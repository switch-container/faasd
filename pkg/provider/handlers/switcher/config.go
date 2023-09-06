package switcher

type SwitcherConfig struct {
	CRIUWorkDirectory string `json:"CRIUWorkDirectory"`
	CRIULogFileName   string `json:"CRIULogFileName"`
	CRIULogLevel      int    `json:"CRIULogLevel"`
}

// example config value
// var Conf Config = Config{
// 	CRIUWorkDirectory: filepath.Join("/root", "switcher-temp", "restore"),
// 	CRIULogFileName:   "restore.log",
// 	CRIULogLevel:      4,
// }
