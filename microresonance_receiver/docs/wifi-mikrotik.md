# Low-Jitter 802.11 Wi-Fi Configuration Guide (ESP32-S3 + RouterOS)

To maintain continuous UDP telemetry and sub-microsecond 802.11 TSF beacon synchronization without key-exchange dropouts (`unicast key exchange timeout`):

## Firmware Configuration Applied in `microresonance_receiver`
- `esp_wifi_set_ps(WIFI_PS_NONE)` and `WiFi.setSleep(false)` disable Wi-Fi modem power-save states so 802.11 beacon frames and outbound UDP bursts experience zero sleep-wake jitter.
- Outbound UDP socket sets `IP_TOS = 0xE0` (**WMM `AC_VO` Voice priority**), bypassing MAC-layer A-MPDU frame aggregation.

## Recommended Access Point Settings (RouterOS / Dedicated 2.4 GHz AP)
1. **Authentication & Encryption:**
   - Use pure `WPA2-PSK` (`aes-ccmp`) rather than mixed `WPA2/WPA3` transition mode.
   - Set **802.11w (PMF / Management Frame Protection)** to `disabled` or `optional` (not `required`).
2. **Group Key Update Interval:**
   - Set `group-key-update` to `1h` (`3600s`) to avoid periodic rekeying stalls during long recording sessions.
