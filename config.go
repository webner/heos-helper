package main

import (
	"io/ioutil"
	"log"

	yaml "gopkg.in/yaml.v2"
)

type HeosConfig struct {
	Player map[int]PlayerConfig `yaml:"player"`
}

type PlayerConfig struct {
	SleepTimer int `json:"sleep_timer" yaml:"sleep_timer"`
}

func (c *HeosConfig) read() *HeosConfig {

	yamlFile, err := ioutil.ReadFile("config.yaml")
	if err != nil {
		log.Printf("yamlFile.Get err   #%v ", err)
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}

	return c
}
