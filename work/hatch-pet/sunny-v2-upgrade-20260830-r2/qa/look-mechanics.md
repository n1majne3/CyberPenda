# Sunny look mechanics

Sunny is a compact humanoid pixel-art pet. Her feet, lower body, and torso stay anchored at the same baseline. Her eyes lead the gaze. Her eyelids and eyebrows support the vertical axis. Her head and neck then turn or pitch slightly, while her dark green cap and long wavy hair follow as attached parts. Her jacket, shirt, jeans, shoes, face proportions, and body volume stay stable. She has no held prop.

Do not rotate, skew, stretch, or tilt the whole sprite. Do not warp the skull, eyes, mouth, cap, hair, hands, or clothing. Preserve the original small pixel-eye construction; do not add eye whites, googly eyes, or a second pupil layer.

## Stable anchor and motion budget

- Keep both feet, the lower torso, the body scale, and the baseline fixed across all 16 directions.
- Use an even 22.5-degree progression. Each adjacent pose changes the same features by a small and similar visual amount.
- Eyes lead by a few source pixels. Eyelids and eyebrows reshape slightly for up and down. The head follows with a restrained turn or pitch. The cap and hair follow the head with slight continuous lag.
- Hair occlusion and cheek visibility change gradually. No adjacent pose may flip the hair, cap, shoulders, or hands to the opposite side.
- Keep the warm cheerful identity, dark outline, crisp pixel edges, compact chibi proportions, and all clothing colors unchanged.

## Cardinal pose families

- `000 up`: eyes and pupils read upward; upper eyelids open slightly; chin lifts a little. The underside of the cap brim becomes slightly more visible. The face remains centered, and the torso and feet do not move.
- `090 screen-right`: pupils, nose tip, mouth center, and face turn clearly toward the screen-right edge. The screen-left cheek and near hair edge become a little more visible; the far screen-right cheek and hair edge become slightly occluded. The cap follows the head without shifting the body.
- `180 down`: eyes and pupils read downward; upper eyelids lower; chin tucks. The cap crown and brim cover slightly more of the forehead, and the front hair settles a little forward. The torso and feet remain fixed.
- `270 screen-left`: pupils, nose tip, mouth center, and face turn clearly toward the screen-left edge. The screen-right cheek and near hair edge become a little more visible; the far screen-left cheek and hair edge become slightly occluded. The cap follows the head without shifting the body.

## Interpolation and continuity

Row 9 progresses clockwise from up through screen-right to one step before down. Row 10 begins at down, progresses through screen-left, and ends one step before up. Diagonals combine both required axes. `157.5 -> 180`, `337.5 -> 000`, and every other adjacent pair must remain one small step apart. No direction may read as neutral/front at the final pet size.
