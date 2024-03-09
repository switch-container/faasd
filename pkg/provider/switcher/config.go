package switcher

type SwitcherConfig struct {
	TargetServiceName string
	CRImageDir        string
	CRWorkDir         string
	CRLogFileName     string
	CRLogLevel        int
	CandidatePID      int
}

// example config value
// var Conf Config = Config{
// 	CRIUWorkDirectory: filepath.Join("/root", "switcher-temp", "restore"),
// 	CRIULogFileName:   "restore.log",
// 	CRIULogLevel:      4,
// }
