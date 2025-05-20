package edgecdnxservices

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/log"
	"gopkg.in/yaml.v3"
)

// init registers this plugin.
func init() { plugin.Register("edgecdnxservices", setup) }

type EdgeCDNXServiceRouting struct {
	FilePath string
	Services []Service
}

type CustomerSpec struct {
	Name string `yaml:"name"`
	Id   int    `yaml:"id"`
}

type ServiceSpec struct {
	Name     string       `yaml:"name"`
	Origins  []string     `yaml:"origins"`
	Customer CustomerSpec `yaml:"customer"`
	Cache    string       `yaml:"cache"`
}

type Service struct {
	Service ServiceSpec `yaml:"service"`
}

// setup is the function that gets called when the config parser see the token "example". Setup is responsible
// for parsing any extra options the example plugin may have. The first token this function sees is "example".
func setup(c *caddy.Controller) error {
	c.Next()

	args := c.RemainingArgs()
	if len(args) != 1 {
		return plugin.Error("edgecdnxservices", c.ArgErr())
	}

	services := &EdgeCDNXServiceRouting{
		FilePath: args[0],
	}

	files, err := filepath.Glob(filepath.Join(services.FilePath, "*.yaml"))
	if err != nil {
		return plugin.Error("edgecdnxservices", err)
	}

	// Process each YAML file (e.g., validate or load into memory)
	for _, file := range files {
		// Example: Log the file name or perform further processing
		log.Debug(fmt.Sprintf("Found YAML file: %s\n", file))

		content, err := os.ReadFile(file)
		if err != nil {
			return plugin.Error("edgecdnxservices", fmt.Errorf("failed to read file %s: %w", file, err))
		}

		var data Service
		if err := yaml.Unmarshal(content, &data); err != nil {
			log.Error(fmt.Sprintf("unmarshal error %v", err))
			return plugin.Error("edgecdnxservices", fmt.Errorf("failed to parse YAML file %s: %w", file, err))
		}

		services.Services = append(services.Services, data)
	}

	// Add the Plugin to CoreDNS, so Servers can use it in their plugin chain.
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		return EdgeCDNXService{Next: next, Services: &services.Services}
	})

	// All OK, return a nil error.
	return nil
}
