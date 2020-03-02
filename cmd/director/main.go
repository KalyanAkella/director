package main

import (
	"flag"
	"io/ioutil"
	"log"

	"github.com/KalyanAkella/director/internal/proxy"
	"gopkg.in/yaml.v2"
)

var (
	configFile string
)

func init() {
	flag.StringVar(&configFile, "configFile", "", "Path to the Director YML config file")
}

func parseConfig() (*proxy.ProxyConfig, error) {
	if data, err := ioutil.ReadFile(configFile); err != nil {
		return nil, err
	} else {
		var director_options proxy.ProxyConfig
		if err := yaml.Unmarshal(data, &director_options); err != nil {
			return nil, err
		} else {
			return &director_options, nil
		}
	}
}

func main() {
	flag.Parse()
	if dir_opts, err := parseConfig(); err != nil {
		log.Fatal(err)
	} else {
		if director, err := proxy.NewDirector(dir_opts); err != nil {
			log.Fatal(err)
		} else {
			log.Fatal(director.ListenAndServe())
		}
	}
}
