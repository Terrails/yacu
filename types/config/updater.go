package config

type Updater struct {
	StopTimeout   int  `yaml:"stop_timeout"`
	RemoveVolumes bool `yaml:"remove_volumes"`
	RemoveImages  bool `yaml:"remove_images"`
}
