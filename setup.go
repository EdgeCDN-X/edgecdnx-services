package edgecdnxservices

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/miekg/dns"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrastructurev1alpha1 "github.com/EdgeCDN-X/edgecdnx-controller/api/v1alpha1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"

	clog "github.com/coredns/coredns/plugin/pkg/log"
)

// init registers this plugin.
func init() { plugin.Register("edgecdnxservices", setup) }

type EdgeCDNXServiceRouting struct {
	Namespace string
	Services  []Service
	Email     string
	Soa       string
}

type CustomerSpec struct {
	Name string `yaml:"name"`
	Id   int    `yaml:"id"`
}

type Service struct {
	Name     string       `yaml:"name"`
	Domain   string       `yaml:"domain"`
	Customer CustomerSpec `yaml:"customer"`
	Cache    string       `yaml:"cache"`
}

// setup is the function that gets called when the config parser see the token "example". Setup is responsible
// for parsing any extra options the example plugin may have. The first token this function sees is "example".
func setup(c *caddy.Controller) error {
	scheme := kruntime.NewScheme()
	clientsetscheme.AddToScheme(scheme)
	infrastructurev1alpha1.AddToScheme(scheme)

	kubeconfig := ctrl.GetConfigOrDie()
	c.Next() // plugin name

	origins := plugin.OriginsFromArgsOrServerBlock(c.RemainingArgs(), c.ServerBlockKeys)

	if len(origins) == 0 {
		origins = []string{"."}
	}

	services := &EdgeCDNXServiceRouting{}

	fmt.Printf("origins, %v\n", origins)
	records := make(map[string][]dns.RR)

	for _, o := range origins {
		records[o] = []dns.RR{}
	}

	//@ 60 IN           SOA             ns.cdn.edgecdnx.com. noc.edgecdnx.com. 2025061801 7200 3600 1209600 3600

	for c.NextBlock() {
		val := c.Val()
		args := c.RemainingArgs()
		if val == "namespace" {
			services.Namespace = args[0]
		}
		if val == "email" {
			services.Email = args[0]
		}
		if val == "soa" {
			services.Soa = args[0]
		}
		if val == "ns" {
			if len(args) != 2 {
				return plugin.Error("edgecdnxservices", fmt.Errorf("expected 2 arguments for ns, got %d", len(args)))
			}
			for _, o := range origins {
				re, err := dns.NewRR(fmt.Sprintf("$ORIGIN %s\n@ IN NS %s\n", o, args[0]))
				re.Header().Name = strings.ToLower(re.Header().Name)
				if err != nil {
					return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create NS record: %w", err))
				}
				records[o] = append(records[o], re)
				re, err = dns.NewRR(fmt.Sprintf("$ORIGIN %s\n%s IN A %s", o, args[0], args[1]))
				re.Header().Name = strings.ToLower(re.Header().Name)
				if err != nil {
					return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create NS record: %w", err))
				}
				records[o] = append(records[o], re)
			}
		}
	}

	for _, o := range origins {
		soa, err := dns.NewRR(fmt.Sprintf("$ORIGIN %s\n@ IN SOA %s.%s %s. 2025061801 7200 3600 1209600 3600", o, services.Soa, o, services.Email))
		if err != nil {
			return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create SOA record: %w", err))
		}
		records[o] = append(records[o], soa)
	}

	clientSet, err := dynamic.NewForConfig(kubeconfig)
	if err != nil {
		return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create dynamic client: %w", err))
	}

	fac := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientSet, 10*time.Minute, services.Namespace, nil)
	informer := fac.ForResource(schema.GroupVersionResource{
		Group:    infrastructurev1alpha1.GroupVersion.Group,
		Version:  infrastructurev1alpha1.GroupVersion.Version,
		Resource: "services",
	}).Informer()

	clog.Infof("edgecdnxservices: Watching Services in namespace %s", services.Namespace)

	sem := &sync.RWMutex{}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			s_raw, ok := obj.(*unstructured.Unstructured)
			if !ok {
				clog.Errorf("edgecdnxservices: expected Service object, got %T", obj)
				return
			}

			temp, err := json.Marshal(s_raw.Object)
			if err != nil {
				clog.Errorf("edgecdnxservices: failed to marshal Service object: %v", err)
				return
			}
			service := &infrastructurev1alpha1.Service{}
			err = json.Unmarshal(temp, service)
			if err != nil {
				clog.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			s := Service{
				Name:   service.Name,
				Domain: service.Spec.Domain,
				Customer: CustomerSpec{
					Name: service.Spec.Customer.Name,
					Id:   service.Spec.Customer.Id,
				},
				Cache: service.Spec.Cache,
			}
			sem.Lock()
			defer sem.Unlock()
			services.Services = append(services.Services, s)
			clog.Infof("edgecdnxservices: Added Service %s", service.Name)
		},
		UpdateFunc: func(oldObj, newObj any) {
			s_new_raw, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				clog.Errorf("edgecdnxservices: expected Service object, got %T", s_new_raw)
				return
			}

			temp, err := json.Marshal(s_new_raw.Object)
			if err != nil {
				clog.Errorf("edgecdnxservices: failed to marshal Service object: %v", err)
				return
			}
			newService := &infrastructurev1alpha1.Service{}
			err = json.Unmarshal(temp, newService)
			if err != nil {
				clog.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()
			for i, service := range services.Services {
				if service.Name == newService.Name {
					services.Services[i] = Service{
						Name:   newService.Name,
						Domain: newService.Spec.Domain,
						Customer: CustomerSpec{
							Name: newService.Spec.Customer.Name,
							Id:   newService.Spec.Customer.Id,
						},
						Cache: newService.Spec.Cache,
					}
					break
				}
			}
			clog.Infof("edgecdnxservices: Updated Service %s", newService.Name)
		},
		DeleteFunc: func(obj any) {
			s_raw, ok := obj.(*unstructured.Unstructured)
			if !ok {
				clog.Errorf("edgecdnxservices: expected Service object, got %T", obj)
				return
			}

			temp, err := json.Marshal(s_raw.Object)
			if err != nil {
				clog.Errorf("edgecdnxservices: failed to marshal Service object: %v", err)
				return
			}
			service := &infrastructurev1alpha1.Service{}
			err = json.Unmarshal(temp, service)
			if err != nil {
				clog.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()
			for i, s := range services.Services {
				if s.Name == service.Name {
					services.Services = append(services.Services[:i], services.Services[i+1:]...)
					break
				}
			}
			clog.Infof("edgecdnxservices: Deleted Service %s", service.Name)
		},
	})

	factoryCloseChan := make(chan struct{})
	fac.Start(factoryCloseChan)

	c.OnShutdown(func() error {
		clog.Infof("edgecdnxservices: shutting down informer")
		close(factoryCloseChan)
		fac.Shutdown()
		return nil
	})

	for _, o := range origins {
		for _, record := range records[o] {
			fmt.Printf("Record: %s\n", record.String())
		}
	}

	// Add the Plugin to CoreDNS, so Servers can use it in their plugin chain.
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		return EdgeCDNXService{Next: next, Services: &services.Services, Sync: sem, InformerSynced: informer.HasSynced, Origins: origins, Records: records}
	})

	// All OK, return a nil error.
	return nil
}
