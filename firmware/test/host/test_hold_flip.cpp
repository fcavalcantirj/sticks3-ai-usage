// firmware/test/host/test_hold_flip.cpp — tests for the pure BtnB hold-to-flip
// state machine.  Verifies click/hold disambiguation, the 1500 ms threshold,
// the 500 ms hold hint, and rotation persistence across flips.
#include "framework.h"
#include "usage/hold_flip.h"

using sticks3::holdflip::HoldFlipDetector;
using sticks3::holdflip::FlipAction;
using sticks3::holdflip::Rotation;

// A click (release before 1500 ms) produces FlipAction::click, not flip.
TEST(hold_short_release_is_click) {
    HoldFlipDetector det;
    uint32_t t = 0;

    ASSERT_EQ(FlipAction::none, det.feed(true, t));     // press at 0
    ASSERT_EQ(FlipAction::none, det.feed(true, t + 200));
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 500));   // hint at 500
    ASSERT_EQ(FlipAction::none, det.feed(true, t + 800));   // no repeat
    ASSERT_EQ(FlipAction::click, det.feed(false, t + 1000)); // release at 1000 < 1500
    ASSERT_EQ(Rotation::upright, det.rotation());
}

// A hold past 1500 ms produces FlipAction::flip and toggles rotation to 3.
TEST(hold_past_threshold_is_flip) {
    HoldFlipDetector det;
    uint32_t t = 0;

    ASSERT_EQ(FlipAction::none, det.feed(true, t));
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 500));    // hint first
    ASSERT_EQ(FlipAction::none, det.feed(true, t + 1499));  // just under, no repeat
    ASSERT_EQ(FlipAction::flip, det.feed(true, t + 1500));   // exactly at threshold
    ASSERT_EQ(Rotation::flipped, det.rotation());
}

// The 500 ms hold hint fires exactly once, before the flip.
TEST(hold_hint_appears_at_500ms) {
    HoldFlipDetector det;
    uint32_t t = 0;

    ASSERT_EQ(FlipAction::none, det.feed(true, t));
    ASSERT_EQ(FlipAction::none, det.feed(true, t + 499));   // just under hint
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 500));   // hint fires
    ASSERT_EQ(FlipAction::none, det.feed(true, t + 1000));  // no repeat
    ASSERT_EQ(FlipAction::none, det.feed(true, t + 1499));  // no repeat
    ASSERT_EQ(FlipAction::flip, det.feed(true, t + 1500));  // flip fires
}

// Two holds return to the original rotation.
TEST(hold_two_flips_return_upright) {
    HoldFlipDetector det;
    uint32_t t = 0;

    // First flip: hint at 500, flip at 1500.
    det.feed(true, t);
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 500));
    ASSERT_EQ(FlipAction::flip, det.feed(true, t + 1500));
    ASSERT_EQ(Rotation::flipped, det.rotation());

    // Release and reset for a second press.
    det.feed(false, t + 1600);

    // Second flip.
    uint32_t t2 = t + 1700;
    det.feed(true, t2);
    ASSERT_EQ(FlipAction::hint, det.feed(true, t2 + 500));
    ASSERT_EQ(FlipAction::flip, det.feed(true, t2 + 1500));
    ASSERT_EQ(Rotation::upright, det.rotation());
}

// A release after the flip threshold does not produce a click.
TEST(hold_release_after_flip_is_noop) {
    HoldFlipDetector det;
    uint32_t t = 0;

    det.feed(true, t);
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 500));
    ASSERT_EQ(FlipAction::flip, det.feed(true, t + 1500));
    // Release after flip → no action.
    ASSERT_EQ(FlipAction::none, det.feed(false, t + 1600));
}

// reset() preserves rotation_.
TEST(hold_reset_preserves_rotation) {
    HoldFlipDetector det;
    uint32_t t = 0;

    det.feed(true, t);
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 500));
    ASSERT_EQ(FlipAction::flip, det.feed(true, t + 1500));
    ASSERT_EQ(Rotation::flipped, det.rotation());

    det.reset();
    ASSERT_EQ(Rotation::flipped, det.rotation());

    // Can flip again after reset.
    det.feed(true, t + 2000);
    ASSERT_EQ(FlipAction::hint, det.feed(true, t + 2500));
    ASSERT_EQ(FlipAction::flip, det.feed(true, t + 3500));
    ASSERT_EQ(Rotation::upright, det.rotation());
}

// The threshold is exactly 1500 ms (not 1499).
TEST(hold_boundary_at_1499_no_flip) {
    HoldFlipDetector det;
    ASSERT_EQ(FlipAction::none, det.feed(true, 0));
    ASSERT_EQ(FlipAction::hint, det.feed(true, 500));    // hint at 500
    ASSERT_EQ(FlipAction::none, det.feed(true, 1499));   // just under threshold, hint already shown
}

// Holding without release never emits click.
TEST(hold_held_3s_no_click) {
    HoldFlipDetector det;
    det.feed(true, 0);
    ASSERT_EQ(FlipAction::hint, det.feed(true, 500));
    ASSERT_EQ(FlipAction::none, det.feed(true, 1000));
    ASSERT_EQ(FlipAction::flip, det.feed(true, 1500));
    ASSERT_EQ(FlipAction::none, det.feed(true, 2000));
    ASSERT_EQ(FlipAction::none, det.feed(true, 3000));
    // Release now → no click (flip already consumed).
    ASSERT_EQ(FlipAction::none, det.feed(false, 3100));
}
