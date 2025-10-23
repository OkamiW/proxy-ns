package buildconfig

import "path/filepath"

var (
	SysConfDir = "/etc"
	ConfigPath = filepath.Join(SysConfDir, "proxy-ns/config.json")
)
