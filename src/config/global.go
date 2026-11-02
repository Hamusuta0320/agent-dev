package config

import "gopkg.in/yaml.v3"

type GlobalConfig struct {
	Provider yaml.Node `yaml:"Provider"`
}
