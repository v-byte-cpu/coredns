package traffic

import (
	"context"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/pkg/dnsutil"
	"github.com/coredns/coredns/plugin/test"
	"github.com/coredns/coredns/plugin/traffic/xds"

	corepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointpb "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/miekg/dns"
	"google.golang.org/grpc"
)

func TestTraffic(t *testing.T) {
	c, err := xds.New("127.0.0.1:0", "test-id", grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	tr := &Traffic{c: c, origins: []string{"lb.example.org."}}

	tests := []struct {
		cla     *endpointpb.ClusterLoadAssignment
		cluster string
		qtype   uint16
		rcode   int
		answer  string // address value of the A/AAAA record.
		ns      bool   // should there be a ns section.
	}{
		{
			cla:     &endpointpb.ClusterLoadAssignment{},
			cluster: "web", qtype: dns.TypeA, rcode: dns.RcodeSuccess, ns: true,
		},
		{
			cla:     &endpointpb.ClusterLoadAssignment{},
			cluster: "web", qtype: dns.TypeSRV, rcode: dns.RcodeSuccess, ns: true,
		},
		{
			cla:     &endpointpb.ClusterLoadAssignment{},
			cluster: "does-not-exist", qtype: dns.TypeA, rcode: dns.RcodeNameError, ns: true,
		},
		// healthy endpoint
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints:   endpoints([]EndpointHealth{{"127.0.0.1", 18008, corepb.HealthStatus_HEALTHY}}),
			},
			cluster: "web", qtype: dns.TypeA, rcode: dns.RcodeSuccess, answer: "127.0.0.1",
		},
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints:   endpoints([]EndpointHealth{{"::1", 18008, corepb.HealthStatus_HEALTHY}}),
			},
			cluster: "web", qtype: dns.TypeAAAA, rcode: dns.RcodeSuccess, answer: "::1",
		},
		// unknown endpoint
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints:   endpoints([]EndpointHealth{{"127.0.0.1", 18008, corepb.HealthStatus_UNKNOWN}}),
			},
			cluster: "web", qtype: dns.TypeA, rcode: dns.RcodeSuccess, ns: true,
		},
		// unknown endpoint and healthy endpoint
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.1", 18008, corepb.HealthStatus_UNKNOWN},
					{"127.0.0.2", 18008, corepb.HealthStatus_HEALTHY},
				}),
			},
			cluster: "web", qtype: dns.TypeA, rcode: dns.RcodeSuccess, answer: "127.0.0.2",
		},
		// unknown endpoint and healthy endpoint, TXT query
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.1", 18008, corepb.HealthStatus_UNKNOWN},
				}),
			},
			cluster: "web", qtype: dns.TypeTXT, rcode: dns.RcodeSuccess, answer: "endpoint-0.web.lb.example.org.",
		},
		// SRV query healthy endpoint
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.2", 18008, corepb.HealthStatus_HEALTHY},
				}),
			},
			cluster: "web", qtype: dns.TypeSRV, rcode: dns.RcodeSuccess, answer: "endpoint-0.web.lb.example.org.",
		},
		// A query for endpoint-0.
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.2", 18008, corepb.HealthStatus_HEALTHY},
				}),
			},
			cluster: "endpoint-0.web", qtype: dns.TypeA, rcode: dns.RcodeSuccess, answer: "127.0.0.2",
		},
		// A query for endpoint-1.
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.2", 18008, corepb.HealthStatus_HEALTHY},
					{"127.0.0.3", 18008, corepb.HealthStatus_HEALTHY},
				}),
			},
			cluster: "endpoint-1.web", qtype: dns.TypeA, rcode: dns.RcodeSuccess, answer: "127.0.0.3",
		},
		// TXT query for _grpc_config
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.2", 18008, corepb.HealthStatus_HEALTHY},
				}),
			},
			cluster: "_grpc_config.web", qtype: dns.TypeTXT, rcode: dns.RcodeSuccess,
		},
	}

	ctx := context.TODO()

	for i, tc := range tests {
		a := xds.NewAssignment()
		a.SetClusterLoadAssignment("web", tc.cla) // web is our cluster
		c.SetAssignments(a)

		m := new(dns.Msg)
		cl := dnsutil.Join(tc.cluster, tr.origins[0])
		m.SetQuestion(cl, tc.qtype)

		rec := dnstest.NewRecorder(&test.ResponseWriter{})
		_, err := tr.ServeDNS(ctx, rec, m)
		if err != nil {
			t.Errorf("Test %d: Expected no error for %q, but got %q", i, cl, err)
		}
		if rec.Msg.Rcode != tc.rcode {
			t.Errorf("Test %d: Expected no rcode %d for %q, but got %d", i, tc.rcode, cl, rec.Msg.Rcode)
		}
		if tc.ns && len(rec.Msg.Ns) == 0 {
			t.Errorf("Test %d: Expected authority section for %q, but got none", i, cl)
		}
		if tc.answer != "" && len(rec.Msg.Answer) == 0 {
			t.Fatalf("Test %d: Expected answer section for %q, but got none", i, cl)
		}
		if tc.answer != "" {
			record := rec.Msg.Answer[0]
			addr := ""
			switch x := record.(type) {
			case *dns.A:
				addr = x.A.String()
			case *dns.AAAA:
				addr = x.AAAA.String()
			case *dns.SRV:
				addr = x.Target
			case *dns.TXT:
				addr = x.Txt[3]
			}
			if tc.answer != addr {
				t.Errorf("Test %d: Expected answer %q for %q, but got %s", i, tc.answer, cl, addr)
			}
		}
	}
}

func TestTrafficSRV(t *testing.T) {
	c, err := xds.New("127.0.0.1:0", "test-id", grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	tr := &Traffic{c: c, origins: []string{"lb.example.org."}}

	tests := []struct {
		cla     *endpointpb.ClusterLoadAssignment
		cluster string
		qtype   uint16
		rcode   int
		answer  int // number of records in answer section
	}{
		// SRV query healthy endpoint
		{
			cla: &endpointpb.ClusterLoadAssignment{
				ClusterName: "web",
				Endpoints: endpoints([]EndpointHealth{
					{"127.0.0.2", 18008, corepb.HealthStatus_HEALTHY},
					{"127.0.0.3", 18008, corepb.HealthStatus_HEALTHY},
				}),
			},
			cluster: "web", qtype: dns.TypeSRV, rcode: dns.RcodeSuccess, answer: 2,
		},
	}

	ctx := context.TODO()

	for i, tc := range tests {
		a := xds.NewAssignment()
		a.SetClusterLoadAssignment("web", tc.cla) // web is our cluster
		c.SetAssignments(a)

		m := new(dns.Msg)
		cl := dnsutil.Join(tc.cluster, tr.origins[0])
		m.SetQuestion(cl, tc.qtype)

		rec := dnstest.NewRecorder(&test.ResponseWriter{})
		_, err := tr.ServeDNS(ctx, rec, m)
		if err != nil {
			t.Errorf("Test %d: Expected no error, but got %q", i, err)
		}
		if rec.Msg.Rcode != tc.rcode {
			t.Errorf("Test %d: Expected no rcode %d, but got %d", i, tc.rcode, rec.Msg.Rcode)
		}
		if tc.answer != len(rec.Msg.Answer) {
			t.Fatalf("Test %d: Expected %d answers, but got %d", i, tc.answer, len(rec.Msg.Answer))
		}
	}
}

func TestTrafficManagement(t *testing.T) {
	c, err := xds.New("127.0.0.1:0", "test-id", grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	tr := &Traffic{c: c, origins: []string{"lb.example.org."}, mgmt: "xds"}

	for _, cla := range []*endpointpb.ClusterLoadAssignment{
		&endpointpb.ClusterLoadAssignment{
			ClusterName: "web",
			Endpoints:   endpoints([]EndpointHealth{{"127.0.0.1", 18008, corepb.HealthStatus_HEALTHY}}),
		},
		&endpointpb.ClusterLoadAssignment{
			ClusterName: "xds",
			Endpoints:   endpoints([]EndpointHealth{{"::1", 18008, corepb.HealthStatus_HEALTHY}}),
		},
	} {
		a := xds.NewAssignment()
		a.SetClusterLoadAssignment(cla.ClusterName, cla)
		c.SetAssignments(a)
	}
	ctx := context.TODO()

	// Now we ask for the grpclb endpoint in the web cluster, this should give us the endpoint of the xds (mgmt) cluster.
	// ; ANSWER SECTION:
	// _grpclb._tcp.web.lb.example.org.	5	IN	SRV	100 100 18008 endpoint-0.xds.lb.example.org.
	// ;; ADDITIONAL SECTION:
	// endpoint-0.xds.lb.example.org.	5	IN	AAAA	::1

	m := new(dns.Msg)
	m.SetQuestion("_grpclb._tcp.web.lb.example.org.", dns.TypeSRV)
	rec := dnstest.NewRecorder(&test.ResponseWriter{})
	if _, err := tr.ServeDNS(ctx, rec, m); err != nil {
		t.Errorf("Expected no error, but got %q", err)
	}
	if len(rec.Msg.Answer) == 0 {
		t.Fatalf("Expected answer section, got none")
	}
	if x := rec.Msg.Answer[0].(*dns.SRV).Target; x != "endpoint-0.xds.lb.example.org." {
		t.Errorf("Expected %s, got %s", "endpoint-0.xds.lb.example.org.", x)
	}
}

type EndpointHealth struct {
	Address string
	Port    uint16
	Health  corepb.HealthStatus
}

func endpoints(e []EndpointHealth) []*endpointpb.LocalityLbEndpoints {
	return endpointsWithLocality(e, xds.Locality{})
}

func endpointsWithLocality(e []EndpointHealth, loc xds.Locality) []*endpointpb.LocalityLbEndpoints {
	ep := make([]*endpointpb.LocalityLbEndpoints, len(e))
	for i := range e {
		ep[i] = &endpointpb.LocalityLbEndpoints{
			Locality: &corepb.Locality{
				Region:  loc.Region,
				Zone:    loc.Zone,
				SubZone: loc.SubZone,
			},
			LbEndpoints: []*endpointpb.LbEndpoint{{
				HostIdentifier: &endpointpb.LbEndpoint_Endpoint{
					Endpoint: &endpointpb.Endpoint{
						Address: &corepb.Address{
							Address: &corepb.Address_SocketAddress{
								SocketAddress: &corepb.SocketAddress{
									Address: e[i].Address,
									PortSpecifier: &corepb.SocketAddress_PortValue{
										PortValue: uint32(e[i].Port),
									},
								},
							},
						},
					},
				},
				HealthStatus: e[i].Health,
			}},
		}
	}
	return ep
}
