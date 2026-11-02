package config

type ProbeConfig struct {
	Type string `json:"Type" yaml:"Type"`
}

type DeepSeekConfig struct {
	Type   string `json:"Type" yaml:"Type"`
	ApiUrl string `json:"ApiUrl" yaml:"ApiUrl"`
	Model  string `json:"Model" yaml:"Model"`
	ApiKey string `json:"ApiKey" yaml:"ApiKey"`
}
