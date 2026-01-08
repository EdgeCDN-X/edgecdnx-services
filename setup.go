package edgecdnxservices

import (
	"encoding/json"
	"fmt"
	"slices"
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

type NSRecord struct {
	Name string
	IPv4 string
	IPv6 string
}

// init registers this plugin.
func init() { plugin.Register("edgecdnxservices", setup) }

func buildZoneRecords(zone infrastructurev1alpha1.Zone, soaRec string, ns []NSRecord) ([]dns.RR, error) {
	zoneNormalized := fmt.Sprintf("%s.", zone.Spec.Zone)

	// NS records + SOA
	recordList := make([]dns.RR, 0)

	serial := time.Now().Format("20060102") + "00"
	// Create SOA Record
	soa, err := dns.NewRR(fmt.Sprintf("$ORIGIN %s\n@ IN SOA %s.%s %s. %s 7200 3600 1209600 3600", zoneNormalized, soaRec, zoneNormalized, zone.Spec.Email, serial))
	if err != nil {
		log.Errorf("edgecdnxservices: failed to create SOA record: %v", err)
		return nil, err
	}
	log.Debugf("edgecdnxservices: Crafted SOA record for zone %s: %s", zoneNormalized, soa.String())
	recordList = append(recordList, soa)

	// Allowed in this block lets continue
	for _, n := range ns {
		re, err := dns.NewRR(fmt.Sprintf("$ORIGIN %s\n@ IN NS %s\n", zoneNormalized, n.Name))
		if err != nil {
			log.Errorf("edgecdnxservices: failed to create NS record: %v", err)
			return nil, err
		}
		recordList = append(recordList, re)
		log.Debugf("edgecdnxservices: Crafted NS record for zone %s: %s", zoneNormalized, re.String())

		re, err = dns.NewRR(fmt.Sprintf("$ORIGIN %s\n%s IN A %s", zoneNormalized, n.Name, n.IPv4))
		if err != nil {
			log.Errorf("edgecdnxservices: failed to create NS A record: %v", err)
			return nil, err
		}
		recordList = append(recordList, re)

		log.Debugf("edgecdnxservices: Crafted NS A record for zone %s: %s", zoneNormalized, re.String())
	}

	return recordList, nil
}

// setup is the function that gets called when the config parser see the token "example". Setup is responsible
// for parsing any extra options the example plugin may have. The first token this function sees is "example".
func setup(c *caddy.Controller) error {
	scheme := kruntime.NewScheme()
	clientsetscheme.AddToScheme(scheme)
	infrastructurev1alpha1.AddToScheme(scheme)

	kubeconfig := ctrl.GetConfigOrDie()
	c.Next() // plugin name

	// Only parse origins from the server block keys
	origins := plugin.OriginsFromArgsOrServerBlock(c.RemainingArgs(), c.ServerBlockKeys)

	// If no origin specified, use default "."
	if len(origins) == 0 {
		origins = []string{"."}
	}

	log.Infof("edgecdnxservices: Origins: %v", origins)
	records := make(map[string][]dns.RR)

	var namespace, soa string // Base Zone configuration
	var ns []NSRecord = make([]NSRecord, 0)

	zones := make([]string, 0)
	services := make([]infrastructurev1alpha1.Service, 0)

	for c.NextBlock() {
		val := c.Val()
		args := c.RemainingArgs()
		if val == "namespace" {
			namespace = args[0]
		}
		if val == "soa" {
			soa = args[0]
		}
		if val == "ns" {
			if len(args) != 2 {
				return plugin.Error("edgecdnxservices", fmt.Errorf("expected 2 arguments for ns, got %d", len(args)))
			}
			ns = append(ns, NSRecord{Name: args[0], IPv4: args[1]})
		}
	}

	clientSet, err := dynamic.NewForConfig(kubeconfig)
	if err != nil {
		return plugin.Error("edgecdnxservices", fmt.Errorf("failed to create dynamic client: %w", err))
	}

	fac := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientSet, 10*time.Minute, namespace, nil)

	sem := &sync.RWMutex{}

	zoneInformer := fac.ForResource(schema.GroupVersionResource{
		Group:    infrastructurev1alpha1.SchemeGroupVersion.Group,
		Version:  infrastructurev1alpha1.SchemeGroupVersion.Version,
		Resource: "zones",
	}).Informer()

	log.Infof("edgecdnxservices: Watching Zones in namespace %s", namespace)

	zoneInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			s_raw, ok := obj.(*unstructured.Unstructured)
			if !ok {
				log.Errorf("edgecdnxservices: expected Zone object, got %T", obj)
				return
			}

			temp, err := json.Marshal(s_raw.Object)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to marshal Zone object: %v", err)
				return
			}
			zone := &infrastructurev1alpha1.Zone{}
			err = json.Unmarshal(temp, zone)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to unmarshal Zone object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()

			zoneNormalized := fmt.Sprintf("%s.", zone.Spec.Zone)
			lookup := plugin.Zones(origins).Matches(zoneNormalized)

			if lookup != "" {
				match := plugin.Zones(zones).Matches(zoneNormalized)
				if match != "" {
					log.Warningf("edgecdnxservices: Zone %s already covered by %s, ignoring", zoneNormalized, match)
					return
				}

				recordList, err := buildZoneRecords(*zone, soa, ns)
				if err != nil {
					log.Errorf("edgecdnxservices: failed to build zone records for zone %s: %v", zoneNormalized, err)
					return
				}

				zones = append(zones, zoneNormalized)
				records[zoneNormalized] = recordList
				log.Infof("edgecdnxservices: Added Zone %s, with the following records: %v", zoneNormalized, recordList)
			} else {
				log.Warningf("edgecdnxservices: Zone %s is not served in this block. Ignoring", zoneNormalized)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			z_new_raw, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				log.Errorf("edgecdnxservices: expected zone object, got %T", z_new_raw)
				return
			}

			temp, err := json.Marshal(z_new_raw.Object)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to marshal zone object: %v", err)
				return
			}
			newZone := &infrastructurev1alpha1.Zone{}
			err = json.Unmarshal(temp, newZone)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to unmarshal zone object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()

			zoneNormalized := fmt.Sprintf("%s.", newZone.Spec.Zone)
			recordList, err := buildZoneRecords(*newZone, soa, ns)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to build zone records for zone %s: %v", zoneNormalized, err)
				return
			}

			delete(records, zoneNormalized)
			records[zoneNormalized] = recordList
		},
		DeleteFunc: func(obj any) {
			z_raw, ok := obj.(*unstructured.Unstructured)
			if !ok {
				log.Errorf("edgecdnxservices: expected Zone object, got %T", obj)
				return
			}

			temp, err := json.Marshal(z_raw.Object)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to marshal Zone object: %v", err)
				return
			}
			zone := &infrastructurev1alpha1.Zone{}
			err = json.Unmarshal(temp, zone)
			if err != nil {
				log.Errorf("edgecdnxservices: failed to unmarshal Service object: %v", err)
				return
			}

			sem.Lock()
			defer sem.Unlock()

			zoneNormalized := fmt.Sprintf("%s.", zone.Spec.Zone)
			delete(records, zoneNormalized)
			zones = slices.DeleteFunc(zones, func(z string) bool {
				return z == zoneNormalized
			})
			log.Infof("edgecdnxservices: Deleted Zone %s", zoneNormalized)
		},
	})

	serviceInformer := fac.ForResource(schema.GroupVersionResource{
		Group:    infrastructurev1alpha1.SchemeGroupVersion.Group,
		Version:  infrastructurev1alpha1.SchemeGroupVersion.Version,
		Resource: "services",
	}).Informer()

	log.Infof("edgecdnxservices: Watching Services in namespace %s", namespace)

	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
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

	// Add the Plugin to CoreDNS, so Servers can use it in their plugin chain.
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		readinessProbes := make([]func() bool, 0)
		redinessProbes := append(readinessProbes, serviceInformer.HasSynced)
		redinessProbes = append(redinessProbes, zoneInformer.HasSynced)

		return EdgeCDNXService{Next: next, Services: &services, Sync: sem, InformersSynced: redinessProbes, Zones: &zones, Records: &records}
	})

	// All OK, return a nil error.
	return nil
}
