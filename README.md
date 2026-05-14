# microresonance

## Experiment: Active Acoustic Probing of the Muscle Waveguide

### Goal
Test whether a control signal can be extracted from muscle not by electrical activity, but by changes in mechanical impedance during active 500 Hz probing.

### Traditional sEMG problem
- sEMG works well in the lab, but in real life it is extremely sensitive to conditions.
- It requires bare skin, conductive gel, and no static load.
- When the user stands up, background noise from postural muscles quickly masks the useful signal.

### New approach
Instead of waiting for nerve impulses, we send a 500 Hz acoustic carrier through the muscle and observe how the tissue's mechanical impedance changes.

### Session 175320 (N=1)
Raw data file: `physio_raw_20260514_175320.jsonl`

Timeline:
- 0–30 s: rest. Sitting motionless. The differential phase between X and Y axes is aligned with only a 1.5° deviation. This is the “acoustic lock.”
- 30–68 s: isometric cycles while seated. Alternate tension and relaxation every 5 seconds. The phase forms distinct steps up to 50°. The gyroscope shows a residual 10°/s — not perfect fixation, but a clear intent signal for a first prototype.
- 68 s: posture change. I straighten the leg while sitting. The waveguide geometry changes, the phase shifts to a new level and instantly stabilizes. I keep clicking — the response persists.
- 107 s: moment of truth — standing up.
  - sEMG: baseline noise increases sharply due to static load, making conscious click detection extremely difficult.
  - Acoustics: the phase continues to produce the same clear steps as while seated. At 500 Hz, muscle electrical noise is irrelevant; the physical contraction of fibers is detected.
- 138 s: recalibration. I sit back down. The phase returns to the initial cluster.

### Why this is potentially cooler than sEMG
- Works through clothing. sEMG requires direct skin contact, while acoustics propagate through mechanical waves.
- You can simply press the sensor against pants or integrate it into an exoskeleton.
- Direct physical metric: with each contraction, the 500 Hz magnitude drops and the muscle dampens the sound as it becomes denser.
- This is a direct measurement of material state, not an indirect measurement of electrical potentials.
- Differential profiling and microsecond synchronization (TSF) allow subtracting global vibration and isolating the pure biomechanical phase shift.

### Results and takeaways
- Yes, this is still N=1.
- Yes, the shape of the phase “glyph” varies between runs.
- But the key fact remains: when EMG starts to fail due to posture change, acoustic impedance still provides a clear signal.

### Next steps
- Analyze the raw JSON/JSONL data.
- Develop a universal gesture alphabet based on phase profiles.
- Evaluate robustness to movement, clothing, and external mechanical interference.

### Data
Raw data is stored in the repository: `physio_raw_20260514_175320.jsonl`

