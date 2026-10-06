package server

// safeKiroSettingValue tries safeKiroSettingValueFor with settingBool then settingInt and
// returns the first non-empty result.
func safeKiroSettingValue(v string) string {
	if r := safeKiroSettingValueFor(v, settingBool); r != "" {
		return r
	}
	return safeKiroSettingValueFor(v, settingInt)
}
