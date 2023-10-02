package switcher

type SwitcherConfig struct {
	CRIUWorkDirectory string
	CRIULogFileName   string
	CRIULogLevel      int
}

// example config value
// var Conf Config = Config{
// 	CRIUWorkDirectory: filepath.Join("/root", "switcher-temp", "restore"),
// 	CRIULogFileName:   "restore.log",
// 	CRIULogLevel:      4,
// }
