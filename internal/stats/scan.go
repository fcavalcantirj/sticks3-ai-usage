package stats

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Scanner scans Claude Code and Codex transcript directories and aggregates
// their token usage into a Report. It is pure: callers supply the clock,
// timezone, and an in-memory Index for incremental re-scans.
type Scanner struct {
	TZ      *time.Location
	Prices  map[string]Price // if nil, uses the embedded table
	Clock   func() time.Time // if nil, time.Now is used
	SkipOld bool             // skip files older than MaxAge (for tests)
}

// NewScanner returns a Scanner with the embedded price table and time.Now.
func NewScanner(tz *time.Location) *Scanner {
	if tz == nil {
		tz = time.UTC
	}
	return &Scanner{
		TZ:     tz,
		Prices: nil, // lookupPrice falls back to embedded table
		Clock:  time.Now,
	}
}

// MaxAge is the cutoff for transcript files (200 days).
const MaxAge = 200 * 24 * time.Hour

// Scan reads the directories in cfg, merging their results into the existing
// index for incremental re-scans. Files older than MaxAge are skipped.
// Returns the new Report and updated Index.
func (s *Scanner) Scan(ctx context.Context, cfg ScanConfig, index Index) (Report, Index, error) {
	now := s.now()
	report := Report{
		GeneratedAt: now.Unix(),
		Sources:     make(map[string]Source),
	}
	if index == nil {
		index = make(Index)
	} else {
		// Copy the input index so we don't mutate the caller's map.
		copied := make(Index, len(index))
		for k, v := range index {
			copied[k] = v
		}
		index = copied
	}

	var errs []error

	if cfg.ClaudeDir != "" {
		claudeSrc, claudeIdx, err := s.scanClaudeCode(ctx, cfg.ClaudeDir, index, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("claude_code scan: %w", err))
		}
		mergeIndex(index, claudeIdx)
		if claudeSrc != nil {
			report.Sources["claude_code"] = *claudeSrc
		}
	}

	if cfg.CodexDir != "" {
		codexSrc, codexIdx, err := s.scanCodex(ctx, cfg.CodexDir, index, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("codex scan: %w", err))
		}
		mergeIndex(index, codexIdx)
		if codexSrc != nil {
			report.Sources["codex"] = *codexSrc
		}
	}

	if len(errs) > 0 {
		return report, index, fmt.Errorf("scan completed with errors: %w", errs[0])
	}
	return report, index, nil
}

// now returns the scanner's clock time, or time.Now.
func (s *Scanner) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// priceOf resolves a model's price, using the per-model map if set or the
// embedded table as fallback.
func (s *Scanner) priceOf(model string) (Price, bool) {
	if s.Prices != nil {
		if p, ok := s.Prices[model]; ok {
			return p, true
		}
	}
	return LookupPrice(model)
}

// mergeIndex folds a per-file index into the persistent index.
func mergeIndex(dst Index, src Index) {
	for k, v := range src {
		dst[k] = v
	}
}

// mergeFileResult merges a FileResult (cached or freshly scanned) into the
// source-level aggregates. It accumulates model tokens/reqs, model order,
// and per-day token breakdowns.
func mergeFileResult(modelAgg *map[string]Tokens, modelReqs *map[string]int, modelOrder *[]string, dayAgg map[string]dayAccum, fr *FileResult) {
	for m, ma := range fr.Models {
		if _, ok := (*modelAgg)[m]; !ok {
			*modelOrder = append(*modelOrder, m)
		}
		(*modelAgg)[m] = (*modelAgg)[m].Add(ma.Tokens)
		(*modelReqs)[m] += ma.Reqs
	}

	for dk, da := range fr.Days {
		d := dayAgg[dk]
		d.tokens = d.tokens.Add(da.Tokens)
		d.reqs += da.Reqs
		if d.byModel == nil {
			d.byModel = make(map[string]Tokens)
		}
		for m, tok := range da.ByModel {
			d.byModel[m] = d.byModel[m].Add(tok)
		}
		if d.date == "" {
			d.date = dk
		}
		dayAgg[dk] = d
	}
}

// --- Claude Code ---

// claudeAssistantLine is the subset of a Claude Code JSONL line we need.
type claudeAssistantLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// scanClaudeCode walks dir for *.jsonl files, parses assistant lines, dedupes
// by message.id + requestId, and aggregates into the report.
//
// Incremental: if a file's size and mtime match the index entry and a cached
// FileResult is present, the file is skipped entirely (read from cache).
func (s *Scanner) scanClaudeCode(ctx context.Context, dir string, index Index, now time.Time) (*Source, Index, error) {
	src := &Source{
		Models: []Model{},
		Days:   []Day{},
		// Ordered via Claude Code plan (Billed via subscription):
		// Codex runs on a ChatGPT Plus subscription, not per-token.
		Billed: false,
		Plan:   "Max",
	}
	modelAgg := make(map[string]Tokens)
	modelReqs := make(map[string]int)
	modelOrder := []string{}
	dayAgg := make(map[string]dayAccum)

	newIdx := make(Index)
	maxAge := now.Add(-MaxAge)
	// Files whose lines exceeded maxScanLine. A non-zero count means the
	// numbers below are incomplete, and the source says so rather than
	// quietly under-reporting — the failure mode this whole guard exists for.
	truncatedFiles := 0
	todayBoundary := dayBoundary(now, s.TZ)
	monthBoundary := monthBoundary(now, s.TZ)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() || !hasSuffix(path, ".jsonl") {
			return nil
		}
		if info.ModTime().Before(maxAge) && !s.SkipOld {
			return nil
		}

		fi := index[path]

		// Cache hit: file unchanged since last scan and we have a cached
		// FileResult — reuse it without opening the file.
		if fi.Size == info.Size() && fi.Mtime == info.ModTime().Unix() && fi.Result != nil {
			mergeFileResult(&modelAgg, &modelReqs, &modelOrder, dayAgg, fi.Result)
			newIdx[path] = fi
			return nil
		}

		// Changed file (or first scan): re-scan.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		fileModels := make(map[string]Tokens)
		fileReqs := make(map[string]int)
		fileDayTokens := make(map[string]Tokens)            // dayKey → total tokens
		fileDayReqs := make(map[string]int)                 // dayKey → request count
		fileDayModels := make(map[string]map[string]Tokens) // dayKey → model → Tokens
		lineNum := fi.Lines

		// If file grew since last scan, only read the appended portion
		// (append-only incremental read). Otherwise read from start.
		var startOffset int64
		if fi.Size > 0 && info.Size() > fi.Size {
			startOffset = fi.Size
		}
		if startOffset > 0 {
			if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
				return err
			}
		}

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 256*1024), maxScanLine)
		seen := make(map[string]bool)
		for sc.Scan() {
			lineNum++
			var line claudeAssistantLine
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				continue
			}
			if line.Type != "assistant" || line.Message.ID == "" {
				continue
			}

			// Dedupe by message.id + requestId. On incremental reads (startOffset>0)
			// we trust that each pair appears once in the appended portion.
			key := line.Message.ID + "|" + line.RequestID
			if seen[key] {
				continue
			}
			seen[key] = true

			tokens := Tokens{
				Input:      line.Message.Usage.InputTokens,
				Output:     line.Message.Usage.OutputTokens,
				CacheWrite: line.Message.Usage.CacheCreationInputTokens,
				CacheRead:  line.Message.Usage.CacheReadInputTokens,
			}

			model := line.Message.Model
			fileModels[model] = fileModels[model].Add(tokens)
			fileReqs[model]++

			dayKey := dayKeyFromTimestamp(line.Timestamp, s.TZ)
			fileDayTokens[dayKey] = fileDayTokens[dayKey].Add(tokens)
			fileDayReqs[dayKey]++
			if fileDayModels[dayKey] == nil {
				fileDayModels[dayKey] = make(map[string]Tokens)
			}
			fileDayModels[dayKey][model] = fileDayModels[dayKey][model].Add(tokens)
		}

		// A TRUNCATED FILE MUST NEVER BE CACHED AS IF IT WERE COMPLETE.
		// sc.Err() is the only place bufio reports a line it could not hold;
		// ignoring it is what let 54% of these transcripts be read in part and
		// stored in the index as the whole truth.
		if err := sc.Err(); err != nil {
			// Skip the file rather than abort the whole scan, and DO NOT write
			// an index entry: a truncated result must not be cached as the
			// whole truth, and the next scan must retry this file.
			f.Close()
			truncatedFiles++
			return nil
		}

		// Build FileResult from the per-file aggregates.
		fr := &FileResult{
			Models: make(map[string]ModelAgg, len(fileModels)),
			Days:   make(map[string]DayAgg, len(fileDayTokens)),
		}
		for m, tok := range fileModels {
			fr.Models[m] = ModelAgg{Tokens: tok, Reqs: fileReqs[m]}
		}
		for dk := range fileDayTokens {
			fr.Days[dk] = DayAgg{
				Tokens:  fileDayTokens[dk],
				Reqs:    fileDayReqs[dk],
				ByModel: fileDayModels[dk],
			}
		}

		// Merge with cached result if present (incremental append).
		if fi.Result != nil {
			mergeFileResult(&modelAgg, &modelReqs, &modelOrder, dayAgg, fi.Result)
		}

		// Merge fresh deltas into source-level aggregates.
		mergeFileResult(&modelAgg, &modelReqs, &modelOrder, dayAgg, fr)

		newIdx[path] = FileIndex{
			Size:   info.Size(),
			Mtime:  info.ModTime().Unix(),
			Lines:  lineNum,
			Result: fr,
		}
		return nil
	})

	if err != nil {
		return nil, newIdx, err
	}

	// Post-walk: compute Today/Month from dayAgg (depends on now, not cacheable).
	// Also accumulate per-model windowed tokens for the Models table.
	todayModelAgg := make(map[string]Tokens)
	monthModelAgg := make(map[string]Tokens)
	for dk, d := range dayAgg {
		dayTS := dayBoundaryFromKey(dk, s.TZ)
		if dayTS >= todayBoundary {
			src.Today.Tokens = src.Today.Tokens.Add(d.tokens)
			src.Today.Requests += d.reqs
			for model, tok := range d.byModel {
				if p, ok := s.priceOf(model); ok {
					src.Today.Cost += p.Cost(tok)
				}
				todayModelAgg[model] = todayModelAgg[model].Add(tok)
			}
		}
		if dayTS >= monthBoundary {
			src.Month.Tokens = src.Month.Tokens.Add(d.tokens)
			src.Month.Requests += d.reqs
			for model, tok := range d.byModel {
				if p, ok := s.priceOf(model); ok {
					src.Month.Cost += p.Cost(tok)
				}
				monthModelAgg[model] = monthModelAgg[model].Add(tok)
			}
		}
	}

	// Build models list (exclude <synthetic>, track unpriced), now with
	// per-model today/month windowed tokens accumulated in the post-walk pass.
	var unpricedModels map[string]bool
	var unpricedToday, unpricedMonth map[string]bool
	src.Models, unpricedModels, unpricedToday, unpricedMonth = buildModelsList(modelOrder, modelAgg, todayModelAgg, monthModelAgg, modelReqs, s.priceOf)
	src.UnpricedToday = sortedKeys(unpricedToday)
	src.UnpricedMonth = sortedKeys(unpricedMonth)

	if truncatedFiles > 0 {
		src.Partial = true
	}

	// Record unpriced models for the caller to warn loudly.
	if len(unpricedModels) > 0 {
		src.UnpricedModels = make([]string, 0, len(unpricedModels))
		for m := range unpricedModels {
			src.UnpricedModels = append(src.UnpricedModels, m)
		}
		sort.Strings(src.UnpricedModels)
		src.Partial = true
	}

	// Build days list (last 182 days, oldest first).
	var dayList []dayEntry
	todayT := time.Unix(todayBoundary, 0).In(s.TZ)
	cutoff := todayT.Add(-181 * 24 * time.Hour)
	for k, d := range dayAgg {
		dayTS := dayBoundaryFromKey(k, s.TZ)
		if dayTS < cutoff.Unix() {
			continue
		}
		dayList = append(dayList, dayEntry{key: k, tokens: d.tokens, date: d.date, byModel: d.byModel})
	}
	sort.Slice(dayList, func(i, j int) bool { return dayList[i].key < dayList[j].key })
	for _, d := range dayList {
		dayCost := 0.0
		for model, tok := range d.byModel {
			if p, ok := s.priceOf(model); ok {
				dayCost += p.Cost(tok)
			}
		}
		dd := Day{
			Date:    d.date,
			Tokens:  d.tokens,
			Cost:    dayCost,
			ByModel: d.byModel,
		}
		src.Days = append(src.Days, dd)
	}

	// Sort models by month tokens descending (matches the Models table header).
	sort.Slice(src.Models, func(i, j int) bool {
		return src.Models[i].TokensMonth.Total() > src.Models[j].TokensMonth.Total()
	})

	// Peak day.
	peakDay, peakTokens := findPeak(dayList)
	src.ActiveDays = len(dayList)
	src.Peak = Peak{Date: peakDay, Tokens: peakTokens}

	return src, newIdx, nil
}

// maxScanLine is the largest JSONL line a transcript may carry.
//
// bufio.Scanner's DEFAULT cap is 64 KiB, and exceeding it makes Scan() stop
// with ErrTooLong — silently, because the error only surfaces via sc.Err(),
// which nothing checked. Measured 2026-09-11 on this machine: 76 of 141 Codex
// transcripts contain a line over 64 KiB and the longest is 9,906,699 bytes,
// so MORE THAN HALF of every session was discarded from the first long line
// onward. The dashboard read "Codex today: 21,993 tokens" while the same
// transcripts, parsed without a line limit, held 24,378,806 input tokens.
//
// Cost is computed from these numbers, so the money figures were understated
// by three orders of magnitude with no error anywhere.
const maxScanLine = 32 * 1024 * 1024

// --- Codex ---

// codexEventLine is the subset of a Codex rollout JSONL line we need.
// The model id lives two places in real Codex data:
//   - on "turn_context" events at payload.model
//   - on "thread_settings_applied" events at payload.thread_settings.model
//
// The token_count event carries "unknown" as the model, so we track the
// most recent model from either source (carry-forward across session turns).
type codexEventLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type string `json:"type"`
		// Model on turn_context events (payload.model, NOT payload.info.model).
		Model string `json:"model"`
		// ThreadSettings on thread_settings_applied events.
		ThreadSettings struct {
			Model string `json:"model"`
		} `json:"thread_settings"`
		// Info.LastTokenUsage on token_count events.
		Info struct {
			Model          string `json:"model"` // "unknown" on token_count lines
			LastTokenUsage struct {
				InputTokens           int64 `json:"input_tokens"`
				CachedInputTokens     int64 `json:"cached_input_tokens"`
				OutputTokens          int64 `json:"output_tokens"`
				ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// scanCodex walks dir for *.jsonl files, parses event_msg + token_count
// lines, sums deltas, and aggregates into the report.
//
// Incremental: if a file's size and mtime match the index entry and a cached
// FileResult is present, the file is skipped entirely (read from cache).
func (s *Scanner) scanCodex(ctx context.Context, dir string, index Index, now time.Time) (*Source, Index, error) {
	src := &Source{
		Models: []Model{},
		Days:   []Day{},
		// Codex traffic runs under a ChatGPT Plus subscription, not per-token:
		Billed: false,
		Plan:   "Plus",
	}
	modelAgg := make(map[string]Tokens)
	modelReqs := make(map[string]int)
	modelOrder := []string{}
	dayAgg := make(map[string]dayAccum)

	newIdx := make(Index)
	maxAge := now.Add(-MaxAge)
	// Files whose lines exceeded maxScanLine. A non-zero count means the
	// numbers below are incomplete, and the source says so rather than
	// quietly under-reporting — the failure mode this whole guard exists for.
	truncatedFiles := 0
	todayBoundary := dayBoundary(now, s.TZ)
	monthBoundary := monthBoundary(now, s.TZ)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() || !hasSuffix(path, ".jsonl") {
			return nil
		}
		if info.ModTime().Before(maxAge) && !s.SkipOld {
			return nil
		}

		fi := index[path]

		// Cache hit: file unchanged since last scan and we have a cached
		// FileResult — reuse it without opening the file.
		if fi.Size == info.Size() && fi.Mtime == info.ModTime().Unix() && fi.Result != nil {
			mergeFileResult(&modelAgg, &modelReqs, &modelOrder, dayAgg, fi.Result)
			newIdx[path] = fi
			return nil
		}

		// Changed file (or first scan): re-scan.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		fileModels := make(map[string]Tokens)
		fileReqs := make(map[string]int)
		fileDayTokens := make(map[string]Tokens)            // dayKey → total tokens
		fileDayReqs := make(map[string]int)                 // dayKey → request count
		fileDayModels := make(map[string]map[string]Tokens) // dayKey → model → Tokens
		lineNum := fi.Lines

		// If file grew since last scan, only read the appended portion
		// (append-only incremental read). Otherwise read from start.
		var startOffset int64
		if fi.Size > 0 && info.Size() > fi.Size {
			startOffset = fi.Size
		}
		if startOffset > 0 {
			if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
				return err
			}
		}

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 256*1024), maxScanLine)
		currentModel := ""
		// BUG 49 fix: the token_count event's timestamp reflects when the
		// response was logged (often "now" due to replay), not when the
		// request was made. Track the day key from the most recent
		// turn_context / thread_settings_applied event (the request time)
		// and use it for day bucketing of token_count events. Fall back to
		// the token_count line's own timestamp if no context was seen.
		currentDayKey := ""
		for sc.Scan() {
			lineNum++
			var line codexEventLine
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				continue
			}
			// BUG 48 fix: real Codex data emits turn_context as a TOP-LEVEL
			// type (line.Type == "turn_context"), not nested inside event_msg.
			// The event_msg guard below would skip these, making model
			// extraction impossible. Handle turn_context first.
			if line.Type == "turn_context" {
				if line.Payload.Model != "" {
					currentModel = line.Payload.Model
				}
				currentDayKey = dayKeyFromTimestamp(line.Timestamp, s.TZ)
				continue
			}

			if line.Type != "event_msg" {
				continue
			}

			// Track the current model from turn_context (nested in event_msg)
			// and thread_settings_applied events. The token_count events carry
			// "unknown" as the model id — the real id lives on the preceding
			// turn_context (payload.model) or thread_settings_applied
			// (payload.thread_settings.model) line. We carry the most recent
			// forward since a session can switch models mid-way.
			if line.Payload.Type == "turn_context" {
				if line.Payload.Model != "" {
					currentModel = line.Payload.Model
				}
				currentDayKey = dayKeyFromTimestamp(line.Timestamp, s.TZ)
				continue
			}
			if line.Payload.Type == "thread_settings_applied" {
				if line.Payload.ThreadSettings.Model != "" {
					currentModel = line.Payload.ThreadSettings.Model
				}
				if currentDayKey == "" {
					currentDayKey = dayKeyFromTimestamp(line.Timestamp, s.TZ)
				}
				continue
			}

			if line.Payload.Type != "token_count" {
				continue
			}

			model := line.Payload.Info.Model
			if model == "unknown" || model == "" {
				model = currentModel
			}
			if model == "" || model == "unknown" {
				// No turn_context seen yet; track as unpriced explicitly.
				model = "<unknown>"
			}
			usage := line.Payload.Info.LastTokenUsage
			tokens := Tokens{
				Input:     usage.InputTokens,
				Output:    usage.OutputTokens + usage.ReasoningOutputTokens,
				CacheRead: usage.CachedInputTokens,
			}

			fileModels[model] = fileModels[model].Add(tokens)
			fileReqs[model]++

			dayKey := currentDayKey
			if dayKey == "" {
				dayKey = dayKeyFromTimestamp(line.Timestamp, s.TZ)
			}
			fileDayTokens[dayKey] = fileDayTokens[dayKey].Add(tokens)
			fileDayReqs[dayKey]++
			if fileDayModels[dayKey] == nil {
				fileDayModels[dayKey] = make(map[string]Tokens)
			}
			fileDayModels[dayKey][model] = fileDayModels[dayKey][model].Add(tokens)
		}

		// A TRUNCATED FILE MUST NEVER BE CACHED AS IF IT WERE COMPLETE.
		// sc.Err() is the only place bufio reports a line it could not hold;
		// ignoring it is what let 54% of these transcripts be read in part and
		// stored in the index as the whole truth.
		if err := sc.Err(); err != nil {
			// Skip the file rather than abort the whole scan, and DO NOT write
			// an index entry: a truncated result must not be cached as the
			// whole truth, and the next scan must retry this file.
			f.Close()
			truncatedFiles++
			return nil
		}

		// Build FileResult from the per-file aggregates.
		fr := &FileResult{
			Models: make(map[string]ModelAgg, len(fileModels)),
			Days:   make(map[string]DayAgg, len(fileDayTokens)),
		}
		for m, tok := range fileModels {
			fr.Models[m] = ModelAgg{Tokens: tok, Reqs: fileReqs[m]}
		}
		for dk := range fileDayTokens {
			fr.Days[dk] = DayAgg{
				Tokens:  fileDayTokens[dk],
				Reqs:    fileDayReqs[dk],
				ByModel: fileDayModels[dk],
			}
		}

		// Merge with cached result if present (incremental append).
		if fi.Result != nil {
			mergeFileResult(&modelAgg, &modelReqs, &modelOrder, dayAgg, fi.Result)
		}

		// Merge fresh deltas into source-level aggregates.
		mergeFileResult(&modelAgg, &modelReqs, &modelOrder, dayAgg, fr)

		newIdx[path] = FileIndex{
			Size:   info.Size(),
			Mtime:  info.ModTime().Unix(),
			Lines:  lineNum,
			Result: fr,
		}
		return nil
	})

	if err != nil {
		return nil, newIdx, err
	}

	// Post-walk: compute Today/Month from dayAgg (depends on now, not cacheable).
	// Also accumulate per-model windowed tokens for the Models table.
	todayModelAgg := make(map[string]Tokens)
	monthModelAgg := make(map[string]Tokens)
	for dk, d := range dayAgg {
		dayTS := dayBoundaryFromKey(dk, s.TZ)
		if dayTS >= todayBoundary {
			src.Today.Tokens = src.Today.Tokens.Add(d.tokens)
			src.Today.Requests += d.reqs
			for model, tok := range d.byModel {
				if p, ok := s.priceOf(model); ok {
					src.Today.Cost += p.Cost(tok)
				}
				todayModelAgg[model] = todayModelAgg[model].Add(tok)
			}
		}
		if dayTS >= monthBoundary {
			src.Month.Tokens = src.Month.Tokens.Add(d.tokens)
			src.Month.Requests += d.reqs
			for model, tok := range d.byModel {
				if p, ok := s.priceOf(model); ok {
					src.Month.Cost += p.Cost(tok)
				}
				monthModelAgg[model] = monthModelAgg[model].Add(tok)
			}
		}
	}

	// Build models list (exclude <synthetic>, track unpriced), now with
	// per-model today/month windowed tokens accumulated in the post-walk pass.
	var unpricedModels map[string]bool
	var unpricedToday, unpricedMonth map[string]bool
	src.Models, unpricedModels, unpricedToday, unpricedMonth = buildModelsList(modelOrder, modelAgg, todayModelAgg, monthModelAgg, modelReqs, s.priceOf)
	src.UnpricedToday = sortedKeys(unpricedToday)
	src.UnpricedMonth = sortedKeys(unpricedMonth)

	if truncatedFiles > 0 {
		src.Partial = true
	}

	// Record unpriced models for the caller to warn loudly.
	if len(unpricedModels) > 0 {
		src.UnpricedModels = make([]string, 0, len(unpricedModels))
		for m := range unpricedModels {
			src.UnpricedModels = append(src.UnpricedModels, m)
		}
		sort.Strings(src.UnpricedModels)
		src.Partial = true
	}

	// Build days list (last 182 days, oldest first).
	var dayList []dayEntry
	todayT := time.Unix(todayBoundary, 0).In(s.TZ)
	cutoff := todayT.Add(-181 * 24 * time.Hour)
	for k, d := range dayAgg {
		dayTS := dayBoundaryFromKey(k, s.TZ)
		if dayTS < cutoff.Unix() {
			continue
		}
		dayList = append(dayList, dayEntry{key: k, tokens: d.tokens, date: d.date, byModel: d.byModel})
	}
	sort.Slice(dayList, func(i, j int) bool { return dayList[i].key < dayList[j].key })
	for _, d := range dayList {
		dayCost := 0.0
		for model, tok := range d.byModel {
			if p, ok := s.priceOf(model); ok {
				dayCost += p.Cost(tok)
			}
		}
		dd := Day{
			Date:    d.date,
			Tokens:  d.tokens,
			Cost:    dayCost,
			ByModel: d.byModel,
		}
		src.Days = append(src.Days, dd)
	}

	// Sort models by month tokens descending (matches the Models table header).
	sort.Slice(src.Models, func(i, j int) bool {
		return src.Models[i].TokensMonth.Total() > src.Models[j].TokensMonth.Total()
	})

	// Peak day.
	peakDay, peakTokens := findPeak(dayList)
	src.ActiveDays = len(dayList)
	src.Peak = Peak{Date: peakDay, Tokens: peakTokens}

	return src, newIdx, nil
}

// --- Helpers ---

// buildModelsList constructs the per-model aggregated list from the scan's
// model-level accumulators. It is shared by scanClaudeCode and scanCodex,
// which differ only in how they parse transcripts but aggregate identically.
//
// The two functions previously carried near-identical copies of this loop —
// that is how PROD-READY 5/10's month-windowing bug returned silently to one
// path and not the other (ORDER #45 precedent). One source of truth here.
//
// Parameters:
//   - modelOrder: models in first-seen order (preserves discovery order)
//   - modelAgg: lifetime token totals per model
//   - todayModelAgg: today-windowed token totals per model
//   - monthModelAgg: month-windowed token totals per model
//   - modelReqs: request counts per model
//   - priceOf: function to resolve a model's price for cost calculation
func buildModelsList(modelOrder []string, modelAgg, todayModelAgg, monthModelAgg map[string]Tokens, modelReqs map[string]int, priceOf func(string) (Price, bool)) ([]Model, map[string]bool, map[string]bool, map[string]bool) {
	models := []Model{}
	unpriced := make(map[string]bool)
	// PER WINDOW, because a tile must only name what it is actually missing.
	// The lifetime union was rendered under BOTH "Cost today" and "Cost this
	// month": on 2026-09-11 the today tile read "(partial: …, fugu)" while fugu
	// had 0 tokens today and 1,142,586 that month. The warning was true of the
	// month and false of the day it was printed under.
	unpricedToday := make(map[string]bool)
	unpricedMonth := make(map[string]bool)
	for _, model := range modelOrder {
		if model == "<synthetic>" {
			continue
		}
		if _, ok := priceOf(model); !ok {
			unpriced[model] = true
			if !todayModelAgg[model].IsZero() {
				unpricedToday[model] = true
			}
			if !monthModelAgg[model].IsZero() {
				unpricedMonth[model] = true
			}
		}
		m := Model{
			Model:       model,
			Tokens:      modelAgg[model],
			Requests:    modelReqs[model],
			TokensToday: todayModelAgg[model],
			TokensMonth: monthModelAgg[model],
		}
		if p, ok := priceOf(model); ok {
			m.Cost = p.Cost(modelAgg[model])
			m.CostMonth = p.Cost(monthModelAgg[model])
		}
		models = append(models, m)
	}
	return models, unpriced, unpricedToday, unpricedMonth
}

// sortedKeys renders a set as a stable, sorted slice — nil when empty, so the
// JSON field is omitted rather than sent as an empty array that a reader could
// mistake for "checked, nothing missing".
func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type dayAccum struct {
	tokens  Tokens
	reqs    int
	date    string
	byModel map[string]Tokens
}

type dayEntry struct {
	key     string
	tokens  Tokens
	date    string
	byModel map[string]Tokens
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// dayKeyFromTimestamp parses an RFC3339 timestamp and returns a day bucket
// key ("YYYY-MM-DD") in the given timezone.
func dayKeyFromTimestamp(ts string, tz *time.Location) string {
	if tz == nil {
		tz = time.UTC
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return ""
		}
	}
	return t.In(tz).Format("2006-01-02")
}

// dayBoundary returns unix seconds for midnight of the given time's day in tz.
func dayBoundary(t time.Time, tz *time.Location) int64 {
	if tz == nil {
		tz = time.UTC
	}
	y, m, d := t.In(tz).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, tz).Unix()
}

// monthBoundary returns unix seconds for the first day of the current month at midnight.
func monthBoundary(t time.Time, tz *time.Location) int64 {
	if tz == nil {
		tz = time.UTC
	}
	y, m, _ := t.In(tz).Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, tz).Unix()
}

// dayBoundaryFromKey parses a "YYYY-MM-DD" key into unix seconds at midnight.
func dayBoundaryFromKey(key string, tz *time.Location) int64 {
	if tz == nil {
		tz = time.UTC
	}
	t, err := time.ParseInLocation("2006-01-02", key, tz)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// findPeak returns the date and token count of the peak day.
func findPeak(days []dayEntry) (string, int64) {
	peakDate := ""
	peakTokens := int64(0)
	for _, d := range days {
		if d.tokens.Total() > peakTokens {
			peakTokens = d.tokens.Total()
			peakDate = d.date
		}
	}
	return peakDate, peakTokens
}

// Ensure io is used (for io.SeekStart in older code paths).
var _ = io.SeekStart
