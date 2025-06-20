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

	"github.com/coredns/coredns/plugin/pkg/log"
)

// init registers this plugin.
func init() { plugin.Register("edgecdnxservices", setup) }

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

	log.Infof("edgecdnxservices: Origins: %v", origins)
	records := make(map[string][]dns.RR)

	for _, o := range origins {
		records[o] = []dns.RR{}
	}

	var namespace, email, soa string
	services := make([]infrastructurev1alpha1.Service, 0)

	for c.NextBlock() {
		val := c.Val()
		args := c.RemainingArgs()
		if val == "namespace" {
			namespace = args[0]
		}
		if val == "email" {
			email = args[0]
		}
		if val == "soa" {
			soa = args[0]
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
		serial := time.Now().Format("20060102") + "00"
		soa, err := dns.NewRR(fmt.Sprintf("$ORIGIN %s\n@ IN SOA %s.%s %s. %s 7200 3600 1209600 3600", o, soa, o, email, serial))
		if err != nil {
			return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create SOA record: %w", err))
		}
		records[o] = append(records[o], soa)
	}

	clientSet, err := dynamic.NewForConfig(kubeconfig)
	if err != nil {
		return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create dynamic client: %w", err))
	}

	fac := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientSet, 10*time.Minute, namespace, nil)
	informer := fac.ForResource(schema.GroupVersionResource{
		Group:    infrastructurev1alpha1.GroupVersion.Group,
		Version:  infrastructurev1alpha1.GroupVersion.Version,
		Resource: "services",
	}).Informer()

	log.Infof("edgecdnxservices: Watching Services in namespace %s", namespace)

	sem := &sync.RWMutex{}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			s_raw, ok := obj.(*unstructured.Unstructured)
			if !ok {
				log.Errorf("edgecdnxservices: expected Service object, got %T", obj)
				return
			}

			temp, err := json.Marshal(s_raw.Object)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to marshal Service object: %v", err)
				return
			}
			service := &infrastructurev1alpha1.Service{}
			err = json.Unmarshal(temp, service)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()
			services = append(services, *service)
			log.Infof("edgecdnxservices: Added Service %s", service.Name)
		},
		UpdateFunc: func(oldObj, newObj any) {
			s_new_raw, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				log.Errorf("edgecdnxservices: expected Service object, got %T", s_new_raw)
				return
			}

			temp, err := json.Marshal(s_new_raw.Object)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to marshal Service object: %v", err)
				return
			}
			newService := &infrastructurev1alpha1.Service{}
			err = json.Unmarshal(temp, newService)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()
			for i, service := range services {
				if service.Name == newService.Name {
					services[i] = *newService
					break
				}
			}
			log.Infof("edgecdnxservices: Updated Service %s", newService.Name)
		},
		DeleteFunc: func(obj any) {
			s_raw, ok := obj.(*unstructured.Unstructured)
			if !ok {
				log.Errorf("edgecdnxservices: expected Service object, got %T", obj)
				return
			}

			temp, err := json.Marshal(s_raw.Object)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to marshal Service object: %v", err)
				return
			}
			service := &infrastructurev1alpha1.Service{}
			err = json.Unmarshal(temp, service)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()
			for i, s := range services {
				if s.Name == service.Name {
					services = append(services[:i], services[i+1:]...)
					break
				}
			}
			log.Infof("edgecdnxservices: Deleted Service %s", service.Name)
		},
	})

	factoryCloseChan := make(chan struct{})
	fac.Start(factoryCloseChan)

	c.OnShutdown(func() error {
		log.Infof("edgecdnxservices: shutting down informer")
		close(factoryCloseChan)
		fac.Shutdown()
		return nil
	})

	for _, o := range origins {
		for _, record := range records[o] {
			log.Infof("edgecdnxservices: Constructed record for zone %s: %s", o, record.String())
		}
	}

	// Add the Plugin to CoreDNS, so Servers can use it in their plugin chain.
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		return EdgeCDNXService{Next: next, Services: &services, Sync: sem, InformerSynced: informer.HasSynced, Origins: origins, Records: records}
	})

	// All OK, return a nil error.
	return nil
}
