// Package example is a CoreDNS plugin that prints "example" to stdout on every packet received.
//
// It serves as an example CoreDNS plugin with numerous code comments.
package edgecdnxservices

import (
	"context"
	"fmt"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
)

// Example is an example plugin to show how to write a plugin.
type EdgeCDNXService struct {
	Next     plugin.Handler
	Services *[]Service
}

type EdgeCDNXServiceResponseWriter struct {
}

func (e EdgeCDNXService) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}
	state.Name()

	log.Debugf("edgecdnxservices %s", state.Name())

	for i := range *e.Services {
		service := (*e.Services)[i]
		if fmt.Sprintf("%s.", service.Name) == state.Name() {
			// Service Exists, lets continue down the chain
			return plugin.NextOrFailure(e.Name(), e.Next, ctx, w, r)
		}
	}

	log.Debugf("edgecdnxservices: service %s not defined in catalogue", state.Name())
	return dns.RcodeServerFailure, nil
}

func (g EdgeCDNXService) Metadata(ctx context.Context, state request.Request) context.Context {
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
