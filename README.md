# Microresonance: Active Acoustic Lock-In Probing of the Muscle Waveguide

[![PlatformIO](https://img.shields.io/badge/Firmware-ESP32--S3%20%7C%20PlatformIO-orange)](microresonance_receiver/)
[![Python](https://img.shields.io/badge/Analysis-Python%203.10%2B-blue)](scripts/)
[![Carrier](https://img.shields.io/badge/Carrier-500%20Hz%20TSF--Locked-success)](microresonance_transceiver/)
[![Data](https://img.shields.io/badge/Raw%20Telemetry-JSONL%20Included-informational)](physio_raw_20260514_175320.jsonl)

**Microresonance** is an experimental non-invasive neuromuscular interface that extracts voluntary motor intent by measuring **active acoustic impedance and phase shift** across skeletal muscle rather than passive electrical potentials (sEMG).

By injecting a phase-continuous **500 Hz mechanical carrier** into the limb and performing coherent **quadrature (I/Q) lock-in demodulation** synchronized via hardware 802.11 Wi-Fi **TSF (Timing Synchronization Function)** clocks (`< 2 µs` jitter), the system achieves a stable acoustic phase **hard-lock** (`99.9%` lock stability) and repeatable isometric step responses akin to Sonomyography (SMG) — while operating dry, through clothing, and remaining resilient to orthostatic postural noise that saturates conventional surface EMG.

---

## 1. Motivation: Why Active Acoustic Probing?

| Modality | Physical Observable | Skin / Coupling Requirement | Orthostatic / Postural Load Resilience | Hardware Complexity |
| :--- | :--- | :--- | :--- | :--- |
| **Passive sEMG** | Motor unit action potentials ($\mu\text{V}$ electrical noise) | Bare skin, conductive gel/dry electrodes, low impedance | **Poor** — standing postural recruitment floods the baseline ($>20\times$ noise floor surge) | Low–Medium |
| **Passive MMG / Accelerometry** | Low-frequency lateral fiber oscillation ($5\text{–}40\text{ Hz}$) | Dry strap | **Poor** — heavily corrupted by limb motion and gravity vector shifts | Low |
| **Ultrasound SMG** | MHz acoustic echogenicity / B-mode cross-section | Acoustic gel, rigid transducer alignment | **High** — measures mechanical deformation directly | High (piezo array, high-voltage pulser, FPGA/DSP) |
| **Microresonance (This Work)** | **Coherent phase velocity & damping of a 500 Hz carrier** | **Dry strap / works through clothing** | **High** — orthogonal to electrical recruitment noise and low-frequency motion ($<50\text{ Hz}$) | **Low** (ESP32-S3, MEMS ADXL355, I2S DAC exciter) |

### Physical Principle

Skeletal muscle behaves as a dispersive viscoelastic waveguide. When a muscle transitions from relaxation to isometric contraction, cross-bridge formation increases both its **transverse/axial stiffness (shear modulus $G$)** and **viscous damping**.
- A shift in shear modulus alters the acoustic phase velocity $v_p = \sqrt{G / \rho}$, producing a deterministic **phase shift ($\Delta\phi$)** over a fixed emitter–receiver baseline $L$:
  $$\Delta \phi = 2\pi f_0 L \left( \frac{1}{v_p(\text{relaxed})} - \frac{1}{v_p(\text{contracted})} \right)$$
- Simultaneously, increased fiber density attenuates the carrier amplitude (**acoustic transparency / magnitude drop**).
- Because the receiver mixes the 4000 Hz accelerometer stream strictly at $f_0 = 500\text{ Hz}$ against a TSF-synchronized local oscillator, low-frequency macro-movements ($0\text{–}50\text{ Hz}$) and stochastic electrical motor-unit firing (sEMG interference) fall outside the narrow lock-in passband.

---

## 2. Experimental Validation: Session `175320`

All raw multi-modal telemetry from the reference session is committed directly in this repository:
- **Raw dataset:** [`physio_raw_20260514_175320.jsonl`](physio_raw_20260514_175320.jsonl) (`5,136` frames, `153.74 s` duration, `33.4 Hz` frame rate, `500.0 Hz` carrier).
- **Reproducible analysis script:** [`scripts/analyze_session.py`](scripts/analyze_session.py)

![Full Session Acoustic Phase vs sEMG Visualization](1778779569118.png)

### Chronological Regime Breakdown

| Time Window | Experimental Protocol | Acoustic Phase Behavior (500 Hz Lock-In) | Reference sEMG Behavior (Neural ADC) |
| :--- | :--- | :--- | :--- |
| **`0 – 30 s`** | **Static Rest (Seated)** | **Acoustic Hard-Lock.** Unwrapped phase holds flat at $\sim 5.3\text{ rad}$; inter-axis differential phase $\Delta\phi_{XY}$ is locked within $\pm 1.5^\circ$. | Low resting baseline floor ($25\text{th pct} \approx 9.8$), sporadic micro-spikes. |
| **`30 – 68 s`** | **Seated Isometric Intent Cycles** (~5 s tension / relaxation) | **Clean Step Response.** Voluntary isometric contractions produce repeatable, square-like phase steps of **$\sim 50^\circ\text{–}58^\circ$** ($\sim 0.9\text{–}1.0\text{ rad}$) and concurrent magnitude damping dips. | Bursts correlate with contractions, though noisy within each hold. |
| **`68 – 105 s`** | **Posture Change (Seated Leg Extension)** | **Waveguide Re-Lock.** At `68 s`, extending the leg alters waveguide geometry; $\Delta\phi_{XY}$ flips by $+180^\circ$ to a new stable plateau and immediately resumes tracking isometric intent steps. | Baseline shifts; spike amplitude becomes less uniform. |
| **`107 – 136 s`** | **Standing Up (Orthostatic Postural Load)** | **Preserved Intent Contrast.** Despite full body-weight load on the leg, conscious isometric contractions continue to produce distinct, flat-topped phase steps (**Zoom Panel 6**). | **Signal Collapse.** Static postural recruitment raises the sEMG baseline floor **20.7×** (`9.8` $\to$ `203.1`), masking voluntary clicks in continuous background tetanus. |
| **`138 – 153.7 s`** | **Seated Recalibration** | Returning to the seated posture restores $\Delta\phi_{XY}$ and unwrapped phase to the initial baseline cluster. | Postural tetanus subsides back to resting floor. |

### Panel-by-Panel Guide to the Figure

1. **Full Session: Neural Intent vs. Acoustic Phase:** Overlays unwrapped 500 Hz acoustic phase (black, radians) against raw sEMG envelope standard deviation (red). Note the clean rectangular steps in acoustic phase throughout both seated (`30–105 s`) and standing (`110–136 s`) regimes.
2. **Acoustic Transparency (Magnitude):** Lock-in carrier amplitude at 500 Hz in gravitational units ($g$). Each isometric contraction increases tissue density, measurably damping carrier transmission ($\sim 3.3\text{ mg} \to 2.1\text{ mg}$).
3. **Motion Dynamics (Gyroscope):** 3-axis angular velocity magnitude ($\text{deg/s}$) from the co-located LSM6DSOX IMU. Confirms that phase steps during `30–68 s`, `72–104 s`, and `110–135 s` occur under quasi-static isometric conditions ($\sim 1^\circ/\text{s}$), while sharp gyroscope spikes (`69 s`, `106 s`, `137 s`) mark macroscopic posture transitions.
4. **Inter-Axis Phase Coherency (XYZ Vector Stability):** Differential lock-in phase between accelerometer axes ($\Delta\phi_{XY} = \phi_X - \phi_Y$, $\Delta\phi_{YZ} = \phi_Y - \phi_Z$). Cancels common-mode sensor translation and acts as an instantaneous geometric posture signature.
5. **Acoustic Reaction Speed (Phase Velocity):** Time derivative $|\text{d}\phi/\text{d}t|$ ($\text{rad/s}$), highlighting rapid mechanical state transitions at contraction onset/release.
6. **ZOOM: Standing & Final Sitting (`100 s – End`):** High-resolution view of the standing orthostatic stress test. Between `110 s` and `135 s`, sEMG saturates into continuous postural noise while the acoustic phase trace resolves three discrete voluntary isometric holds (`114–120 s`, `123–130 s`, `130–135 s`).
7. **The Universal Bio-Acoustic Alphabet (Phase vs. Mag Space):** Joint phase–magnitude state-space projection colored by sEMG intensity, revealing discrete, separable attractors corresponding to biomechanical states (relaxed seated, contracted seated, extended, standing loaded).

---

## 3. Hardware & Embedded System Architecture

The hardware consists of two wireless **ESP32-S3** nodes synchronized over standard 2.4 GHz Wi-Fi without any interconnecting cable between emitter and detector.

| Active Acoustic Emitter Belt (`microresonance_transceiver`) | Coherent Multi-Modal Receiver Strap (`microresonance_receiver`) |
| :---: | :---: |
| ![Emitter belt assembly](IMG20260514213337.jpg) | ![Receiver and detector strap](IMG20260514213901.jpg) |

### Key Firmware Engineering Solutions

Achieving sub-degree acoustic phase lock across independent wireless microcontrollers required solving three hardware-level bottlenecks in the firmware:

#### 1. Zero-Drift Wireless Coherency via 802.11 TSF Absolute-Phase NCO
Free-running crystal oscillators on separate MCUs drift by $10\text{–}20\text{ ppm}$ ($1800^\circ/\text{s}$ phase walk at $500\text{ Hz}$), destroying lock-in coherence within milliseconds.
- **Emitter ([`microresonance_transceiver/src/main.cpp`](microresonance_transceiver/src/main.cpp)):** Queries the hardware 802.11 Wi-Fi Beacon **Timing Synchronization Function (`esp_wifi_get_tsf_time`)** before each I2S DMA buffer and computes the exact analytical carrier phase for every sample $i$ at $44.1\text{ kHz}$:
  $$\phi_i = 2\pi \cdot \left( \left( t_{\text{TSF}} \cdot 10^{-6} + \frac{i}{f_s} \right) f_{\text{carrier}} \bmod 1.0 \right)$$
- Driven through a 4-wire **PCM5102A I2S DAC** using the ESP32-S3 **APLL** (`use_apll = true`) and external Master Clock (`MCLK` on `GPIO 15`).
- **Receiver ([`microresonance_receiver/src/dsp_task.cpp`](microresonance_receiver/src/dsp_task.cpp)):** Captures the rising edge of the **ADXL355** hardware `DRDY` pin (`GPIO 13`) in an IRAM ISR (`drdy_isr`), measures the exact ISR-to-SPI readout lag `drdyAgeUs`, and back-dates the Wi-Fi TSF timestamp (`tsf = tsfAfter - drdyAgeUs`).

#### 2. Asymmetric Dual-Core FreeRTOS Isolation (Eliminating 3.3V ADC Rail Cross-Talk)
At $20\text{-bit}$ resolution ($\sim 3.9\,\mu g/\text{LSB}$), the **ADXL355** accelerometer is sensitive enough that calling `analogRead()` on the ESP32-S3 SAR ADC for sEMG measurement induces transient current spikes on the shared $3.3\text{ V}$ supply rail, appearing as synthetic vibration noise.
- **Core 1 (`sensorTask` @ Prio 5, `dspTask` @ Prio 4):** Dedicated exclusively to deterministic $4000\text{ Hz}$ SPI bursts from the ADXL355 and non-blocking UDP dispatch. Zero ADC reads or I2C stalls occur on Core 1.
- **Core 0 (`lsmTask` @ Prio 3):** Polls the **LSM6DSOX** 6-axis IMU (`400 kHz` I2C + hardware 24-bit timestamp registers) and samples the analog **sEMG** channel (`GPIO 1`) into a lock-free ring buffer indexed by `g_active_ab`.

#### 3. MTU-Safe UDP Streaming with WMM `AC_VO` Voice QoS
Standard Wi-Fi MAC layers buffer outbound packets into A-MPDU aggregates (up to 16 frames), introducing $15\text{–}60\text{ ms}$ burst jitter.
- **Wire Protocol ([`microresonance_receiver/include/dsp_packet.h`](microresonance_receiver/include/dsp_packet.h)):** Packs $120\times 3\text{-axis}$ ADXL355 samples ($30\text{ ms}$ window), $30\times 6\text{-axis}$ LSM6DSOX IMU samples + timestamps, and $120$ sEMG samples into a packed **1460-byte `RawSyncPacket` (`0x4D58`)** — fitting inside a single IPv4 UDP datagram (`< 1472` bytes) with zero IP fragmentation.
- Tags the UDP socket with `IP_TOS = 0xE0` (**WMM `AC_VO` Voice priority**), instructing the Wi-Fi baseband to disable A-MPDU aggregation and transmit each $30\text{ ms}$ window immediately.

---

## 4. Repository Structure & Quick Start

```text
microresonance/
├── microresonance_transceiver/       # ESP32-S3 Active Acoustic Emitter Firmware (PlatformIO)
│   ├── include/                      # Pinout config, TSF clock helper, AD9833 & UI state headers
│   ├── src/                          # TSF-locked NCO audio engine (I2S PCM5102A), OLED UI, HTTP sync
│   └── platformio.ini                # Build config for ESP32-S3-CAM (N16R8)
├── microresonance_receiver/          # ESP32-S3 Coherent Multi-Modal Receiver Firmware (PlatformIO)
│   ├── include/                      # RawSyncPacket (0x4D58) wire format, DSP task & ADS8681 headers
│   ├── src/                          # Dual-core ADXL355 SPI + LSM6DSOX I2C + sEMG capture & UDP streamer
│   └── platformio.ini                # Build config for ESP32-S3 (FH4R2)
├── scripts/                          # Python Signal Interpretation & Visualization Suite
│   ├── analyze_session.py            # Reproduces the 7-panel plot & regime metrics from raw JSONL
│   └── lockin_dsp_reference.py       # Reference RawSyncPacket parser & TSF-coherent I/Q demodulator
├── physio_raw_20260514_175320.jsonl  # Raw multi-modal experimental dataset (5,136 frames, 153.7s)
├── 1778779569118.png                 # Generated 7-panel visualization from Session 175320
├── IMG20260514213337.jpg             # Hardware photo: Emitter belt assembly
├── IMG20260514213901.jpg             # Hardware photo: Receiver / detector strap
└── requirements.txt                  # Python dependencies (numpy, matplotlib)
```

### Reproduce the Session Analysis & Plots

```bash
# 1. Create a virtual environment and install dependencies
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt

# 2. Run the session analyzer on the raw JSONL dump (prints regime report & generates PNG)
python3 scripts/analyze_session.py \
    --input physio_raw_20260514_175320.jsonl \
    --output 1778779569118.png

# 3. Run the mathematical self-test of the TSF-coherent I/Q lock-in demodulator
python3 scripts/lockin_dsp_reference.py --self-test
```

### Sample Console Output (`scripts/analyze_session.py`)

```text
========================================================================================
MICRORESONANCE TELEMETRY REPORT — physio_raw_20260514_175320.jsonl
========================================================================================
Frames: 5136  |  Duration: 153.74 s  |  Frame interval: 29.93 ms  |  Carrier: 500.0 Hz  |  Hard-Lock: 99.9%
----------------------------------------------------------------------------------------
Experimental Regime                | Window (s)   | Phase Range (rad) | Mean Mag (mg) | sEMG RMS/Std
----------------------------------------------------------------------------------------
1. Static Rest (Acoustic Lock)     |   0.0–30.0   |             0.353 |         2.657 |         91.4
2. Seated Isometric Cycles         |  30.0–68.0   |             1.093 |         3.293 |         84.0
3. Seated Leg Extension (Re-Lock)  |  70.0–105.0  |             0.964 |         2.575 |         36.7
4. Standing Orthostatic Load       | 110.0–136.0  |             1.910 |         2.324 |        260.8
5. Seated Recalibration            | 140.0–153.7  |             0.798 |         2.106 |         50.6
----------------------------------------------------------------------------------------
Key Comparative Findings (Seated vs. Standing Orthostatic Load):
  * sEMG Baseline Floor (25th pct):   9.8 (seated) -> 203.1 (standing) [20.7x postural noise surge]
  * Acoustic Phase Dynamic Step:      57.8 deg (seated) vs. 103.8 deg (standing) [step contrast preserved]
  * Peak Postural Transition Gyro:    159.0 deg/s (settled to 1.03 deg/s during standing holds)
========================================================================================
```

---

## 5. Current Scope, Limitations & Roadmap

- **Current Validation Scope:** The dataset included here represents an $N=1$ proof-of-principle demonstrating coherent phase hard-lock and orthostatic noise immunity under controlled seated-to-standing transitions.
- **Waveguide Geometry Calibration:** Macroscopic changes in joint angle alter the physical path length between exciter and receiver, shifting the absolute baseline phase to a new attractor (as seen at `68 s` and `138 s`). While inter-axis differential phase ($\Delta\phi_{XY}, \Delta\phi_{YZ}$) and IMU fusion cleanly segment these posture transitions, automated baseline re-zeroing across continuous dynamic motion is an active focus.
- **Next Engineering Milestones:**
  1. **Dual-Frequency MIMO Differential Probing (`f1` / `f2`):** Using beat-phase dispersion ($\Delta\phi_{\text{beat}}$) between two simultaneous carriers (e.g., `311 Hz` and `500 Hz`) to cancel geometric path-length changes without relying on IMU gating.
  2. **Multi-Subject & Multi-DoF Gesture Alphabet:** Expanding state-space clustering in $(\phi, \text{Mag}, \Delta\phi_{XY})$ space to classify fine finger and wrist isometric intents through clothing.
  3. **Single-Strap Wearable Integration:** Miniaturizing the exciter and ADXL355 detector into a unified dry elastomer band for exoskeleton, prosthetics, and AR/VR human-computer interaction.

---

## Recommended GitHub Repository Topics / Tags

`non-invasive-bci` `human-computer-interaction` `biomedical-signal-processing` `acoustic-impedance` `sonomyography` `semg` `lock-in-amplifier` `esp32-s3` `adxl355` `neurotech` `wearable-sensors` `biomechanics` `embedded-dsp`
