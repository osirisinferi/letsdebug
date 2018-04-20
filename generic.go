package letsdebug

import (
	"strings"

	"fmt"

	"github.com/miekg/dns"
	"golang.org/x/net/publicsuffix"
)

type caaChecker struct{}

func (c caaChecker) Check(ctx *scanContext, domain string, method ValidationMethod) ([]Problem, error) {
	var probs []Problem

	wildcard := false
	if strings.HasPrefix(domain, "*.") {
		wildcard = true
		domain = domain[2:]
	}

	rrs, err := ctx.Lookup(domain, dns.TypeCAA)
	if err != nil {
		probs = append(probs, dnsLookupFailed(domain, "CAA", err))
		return probs, nil
	}

	// check any found caa records
	if len(rrs) > 0 {
		var issue []*dns.CAA
		var issuewild []*dns.CAA
		var criticalUnknown []*dns.CAA

		for _, rr := range rrs {
			caaRr, ok := rr.(*dns.CAA)
			if !ok {
				continue
			}

			switch caaRr.Tag {
			case "issue":
				issue = append(issue, caaRr)
			case "issuewild":
				issuewild = append(issuewild, caaRr)
			case "iodef":
				// TODO: should this print a notice that lets encrypt doesn't support iodef atm?
				// https://github.com/letsencrypt/boulder/issues/2580
			default:
				if caaRr.Flag == 1 {
					criticalUnknown = append(criticalUnknown, caaRr)
				}
			}
		}

		if len(criticalUnknown) > 0 {
			probs = append(probs, caaCriticalUnknown(domain, wildcard, criticalUnknown))
			return probs, nil
		}

		if len(issue) == 0 && !wildcard {
			return probs, nil
		}

		records := issue
		if wildcard && len(issuewild) > 0 {
			records = issuewild
		}

		for _, r := range records {
			if extractIssuerDomain(r.Value) == "letsencrypt.org" {
				return probs, nil
			}
		}

		probs = append(probs, caaIssuanceNotAllowed(domain, wildcard, records))

		return probs, nil
	}

	// recurse up to the public suffix domain until a caa record is found
	// a.b.c.com -> b.c.com -> c.com until
	if ps, _ := publicsuffix.PublicSuffix(domain); domain != ps {
		splitDomain := strings.SplitN(domain, ".", 2)

		parentProbs, err := c.Check(ctx, splitDomain[1], method)
		if err != nil {
			return nil, fmt.Errorf("error checking caa record on domain: %s, %v", splitDomain[1], err)
		}

		probs = append(probs, parentProbs...)
	}

	return probs, nil
}

func extractIssuerDomain(value string) string {
	// record can be:
	// issuedomain.tld; someparams
	return strings.Trim(strings.SplitN(value, ";", 2)[0], " \t")
}

func collateRecords(records []*dns.CAA) string {
	var s []string
	for _, r := range records {
		s = append(s, r.String())
	}
	return strings.Join(s, "\n")
}

func caaCriticalUnknown(domain string, wildcard bool, records []*dns.CAA) Problem {
	return Problem{
		Name: "CaaCriticalUnknown",
		Explanation: fmt.Sprintf(`CAA record(s) exist on %s (wildcard=%t) that are marked as critical but are unknown to Let's Encrypt. `+
			`These record(s) as shown in the detail must be removed, or marked as non-critical, before a certificate can be issued by the Let's Encrypt CA.`, domain, wildcard),
		Detail:   collateRecords(records),
		Severity: SeverityFatal,
	}
}

func caaIssuanceNotAllowed(domain string, wildcard bool, records []*dns.CAA) Problem {
	return Problem{
		Name: "CaaIssuanceNotAllowed",
		Explanation: fmt.Sprintf(`No CAA record on %s (wildcard=%t) contains the issuance domain "letsencrypt.org". `+
			`You must either add an additional record to include "letsencrypt.org" or remove every existing CAA record. `+
			`A list of the CAA records are provided in the details.`, domain, wildcard),
		Detail:   collateRecords(records),
		Severity: SeverityFatal,
	}
}
