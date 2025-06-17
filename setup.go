package edgecdnxservices

import (
	"context"
	"fmt"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"

	"k8s.io/apimachinery/pkg/runtime"

	infrastructurev1alpha1 "github.com/EdgeCDN-X/edgecdnx-controller/api/v1alpha1"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// init registers this plugin.
func init() { plugin.Register("edgecdnxservices", setup) }

type EdgeCDNXServiceRouting struct {
	Namespace string
	Services  []Service
}

type CustomerSpec struct {
	Name string `yaml:"name"`
	Id   int    `yaml:"id"`
}

type Service struct {
	Name     string       `yaml:"name"`
	Customer CustomerSpec `yaml:"customer"`
	Cache    string       `yaml:"cache"`
}

// setup is the function that gets called when the config parser see the token "example". Setup is responsible
// for parsing any extra options the example plugin may have. The first token this function sees is "example".
func setup(c *caddy.Controller) error {
	scheme := runtime.NewScheme()
	clientsetscheme.AddToScheme(scheme)
	infrastructurev1alpha1.AddToScheme(scheme)

	kubeconfig := ctrl.GetConfigOrDie()
	kubeclient, err := client.New(kubeconfig, client.Options{Scheme: scheme})
	if err != nil {
		return plugin.Error("edgecdnxprefixlist", fmt.Errorf("failed to create Kubernetes client: %w", err))
	}

	c.Next()

	args := c.RemainingArgs()
	if len(args) != 1 {
		return plugin.Error("edgecdnxservices", c.ArgErr())
	}

	services := &EdgeCDNXServiceRouting{
		Namespace: args[0],
	}

	kserviceList := &infrastructurev1alpha1.ServiceList{}
	if err := kubeclient.List(context.TODO(), kserviceList, &client.ListOptions{
		Namespace: services.Namespace,
	}); err != nil {
		return plugin.Error("edgecdnxservices", fmt.Errorf("failed to list Services: %w", err))
	}

	for _, service := range kserviceList.Items {
		s := Service{
			Name: service.Name,
			Customer: CustomerSpec{
				Name: service.Spec.Customer.Name,
				Id:   service.Spec.Customer.Id,
			},
			Cache: service.Spec.Cache,
		}
		services.Services = append(services.Services, s)
	}

	// Add the Plugin to CoreDNS, so Servers can use it in their plugin chain.
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		return EdgeCDNXService{Next: next, Services: &services.Services}
	})

	// All OK, return a nil error.
	return nil
}
