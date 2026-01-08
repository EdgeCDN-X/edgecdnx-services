// Package example is a CoreDNS plugin that prints "example" to stdout on every packet received.
//
// It serves as an example CoreDNS plugin with numerous code comments.
package edgecdnxservices

import (
	"context"
	"fmt"
	"sync"

	infrastructurev1alpha1 "github.com/EdgeCDN-X/edgecdnx-controller/api/v1alpha1"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/metadata"
	"github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
)

// Example is an example plugin to show how to write a plugin.
type EdgeCDNXService struct {
	Next            plugin.Handler
	Services        *[]infrastructurev1alpha1.Service
	Sync            *sync.RWMutex
	InformersSynced []func() bool
	Zones           *[]string
	Records         *map[string][]dns.RR
}

type EdgeCDNXServiceResponseWriter struct {
}

func (e EdgeCDNXService) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}
	qname := state.Name()

	e.Sync.RLock()
	defer e.Sync.RUnlock()

	for i := range *e.Services {
		service := (*e.Services)[i]
		if fmt.Sprintf("%s.", service.Spec.Domain) == qname && (state.QType() == dns.TypeA || state.QType() == dns.TypeAAAA) {
			// Service Exists, lets continue down the chain
			return plugin.NextOrFailure(e.Name(), e.Next, ctx, w, r)
		}

		for _, ha := range service.Spec.HostAliases {
			if fmt.Sprintf("%s.", ha.Name) == qname && (state.QType() == dns.TypeA || state.QType() == dns.TypeAAAA) {
				log.Debugf("HostAlias %s found for domain %s", ha.Name, service.Spec.Domain)
				// HostAlias Exists, lets continue down the chain
				return plugin.NextOrFailure(e.Name(), e.Next, ctx, w, r)
			}
		}
	}

	zone := plugin.Zones(*e.Zones).Matches(qname)

	if zone == "" {
		return dns.RcodeServerFailure, nil
	} else {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		nxdomain := true
		var soa dns.RR
		for _, r := range (*e.Records)[zone] {
			if r.Header().Rrtype == dns.TypeSOA && soa == nil {
				soa = r
			}
			if r.Header().Name == qname {
				nxdomain = false
				if r.Header().Rrtype == state.QType() {
					m.Answer = append(m.Answer, r)
				}
			}
		}

		// handle NXDOMAIN, NODATA and normal response here.
		if nxdomain {
			m.Rcode = dns.RcodeNameError
			if soa != nil {
				m.Ns = []dns.RR{soa}
			}
			w.WriteMsg(m)
			return dns.RcodeSuccess, nil
		}

		if len(m.Answer) == 0 {
			if soa != nil {
				m.Ns = []dns.RR{soa}
			}
		}

		w.WriteMsg(m)
		return dns.RcodeSuccess, nil
	}
}

func (g EdgeCDNXService) setServiceMeta(ctx context.Context, service *infrastructurev1alpha1.Service) context.Context {
	metadata.SetValueFunc(ctx, g.Name()+"/customer", func() string {
		return fmt.Sprintf("%d", service.Spec.Customer.Id)
	})

	metadata.SetValueFunc(ctx, g.Name()+"/cache", func() string {
		return service.Spec.Cache
	})

	return ctx
}

func (g EdgeCDNXService) Metadata(ctx context.Context, state request.Request) context.Context {
	for i := range *g.Services {
		service := (*g.Services)[i]
		if fmt.Sprintf("%s.", service.Spec.Domain) == state.Name() {
			return g.setServiceMeta(ctx, &service)
		}

		for _, ha := range service.Spec.HostAliases {
			if fmt.Sprintf("%s.", ha.Name) == state.Name() {
				return g.setServiceMeta(ctx, &service)
			}
		}
	}

	return ctx
}

// Name implements the Handler interface.
func (e EdgeCDNXService) Name() string { return "edgecdnxservices" }

// ResponsePrinter wrap a dns.ResponseWriter and will write example to standard output when WriteMsg is called.
type ResponsePrinter struct {
	dns.ResponseWriter
}

// NewResponsePrinter returns ResponseWriter.
func NewResponsePrinter(w dns.ResponseWriter) *ResponsePrinter {
	return &ResponsePrinter{ResponseWriter: w}
}

// WriteMsg calls the underlying ResponseWriter's WriteMsg method and prints "example" to standard output.
func (r *ResponsePrinter) WriteMsg(res *dns.Msg) error {
	log.Info("edgecdnxservices")
	return r.ResponseWriter.WriteMsg(res)
}
