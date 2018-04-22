package letsdebug

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	httpTimeout = 10
)

type redirectError string

func (e redirectError) Error() string {
	return string(e)
}

type httpCheckResult struct {
	StatusCode   int
	ServerHeader string
	IP           net.IP
}

func (r httpCheckResult) IsZero() bool {
	return r.StatusCode == 0
}

func (r httpCheckResult) String() string {
	addrType := "IPv6"
	if r.IP.To4() != nil {
		addrType = "IPv4"
	}
	return fmt.Sprintf("[Address Type=%s,Response Code=%d,Server=%s]", addrType, r.StatusCode, r.ServerHeader)
}

func checkHTTP(domain string, address net.IP) (httpCheckResult, Problem) {
	dialer := net.Dialer{
		Timeout: httpTimeout * time.Second,
	}

	checkRes := httpCheckResult{
		IP: address,
	}
	var redirErr redirectError

	cl := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				if address.To4() == nil {
					return dialer.DialContext(ctx, "tcp", "["+address.String()+"]:"+port)
				}
				return dialer.DialContext(ctx, "tcp", address.String()+":"+port)
			},
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		// boulder: va.go fetchHTTP
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				redirErr = redirectError(fmt.Sprintf("Too many (%d) redirects, last redirect was to: %s", len(via), req.URL.String()))
				return redirErr
			}

			host := req.URL.Host
			if _, p, err := net.SplitHostPort(host); err == nil {
				if port, _ := strconv.Atoi(p); port != 80 && port != 443 {
					redirErr = redirectError(fmt.Sprintf("Bad port number provided when fetching %s: %s", req.URL.String(), p))
					return redirErr
				}
			}

			scheme := strings.ToLower(req.URL.Scheme)
			if scheme != "http" && scheme != "https" {
				redirErr = redirectError(fmt.Sprintf("Bad scheme provided when fetching %s: %s", req.URL.String(), scheme))
				return redirErr
			}

			// Also check for domain.tld.well-known/acme-challenge
			if strings.HasSuffix(req.URL.Hostname(), ".well-known") {
				redirErr = redirectError(fmt.Sprintf("It appears that a redirect was generated by your web server that is missing a trailing "+
					"slash after your domain name: %v. Check your web server configuration and .htaccess for Redirect/RedirectMatch/RewriteRule.",
					req.URL.String()))
				return redirErr
			}

			return nil
		},
	}

	req, err := http.NewRequest("GET", "http://"+domain+"/.well-known/acme-challenge/letsdebug-test", nil)
	if err != nil {
		return checkRes, internalProblem(fmt.Sprintf("Failed to construct validation request: %v", err), SeverityError)
	}

	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "github.com/alexzorin/letsdebug")

	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout*time.Second)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := cl.Do(req)
	if resp != nil {
		checkRes.StatusCode = resp.StatusCode
		checkRes.ServerHeader = resp.Header.Get("Server")
	}
	if err != nil {
		if redirErr != "" {
			err = redirErr
		}
		return checkRes, translateHTTPError(domain, address, err)
	}

	defer resp.Body.Close()

	return checkRes, Problem{}
}

func translateHTTPError(domain string, address net.IP, e error) Problem {
	if redirErr, ok := e.(redirectError); ok {
		return badRedirect(domain, redirErr)
	}

	if strings.HasSuffix(e.Error(), "http: server gave HTTP response to HTTPS client") {
		return httpServerMisconfiguration(domain, "Web server is serving the wrong protocol on the wrong port: "+e.Error()+
			". This may be due to a previous HTTP redirect rather than a webserver misconfiguration.")
	}

	if address.To4() == nil {
		return aaaaNotWorking(domain, address.String(), e)
	} else {
		return aNotWorking(domain, address.String(), e)
	}
}

func httpServerMisconfiguration(domain, detail string) Problem {
	return Problem{
		Name:        "WebserverMisconfiguration",
		Explanation: fmt.Sprintf(`%s's webserver may be misconfigured.`, domain),
		Detail:      detail,
		Severity:    SeverityError,
	}
}

func aaaaNotWorking(domain, ipv6Address string, err error) Problem {
	return Problem{
		Name: "AAAANotWorking",
		Explanation: fmt.Sprintf(`%s has an AAAA (IPv6) record (%s) but a test ACME validation request over port 80 has revealed problems. `+
			`Let's Encrypt will prefer to use AAAA records, if present, and will not fall back to IPv4 records. `+
			`You should either ensure that validation requests succeed over IPv6, or remove its AAAA record.`,
			domain, ipv6Address),
		Detail:   err.Error(),
		Severity: SeverityError,
	}
}

func aNotWorking(domain, addr string, err error) Problem {
	return Problem{
		Name: "ANotWorking",
		Explanation: fmt.Sprintf(`%s has an A (IPv4) record (%s) but a test ACME validation request over port 80 has revealed problems.`,
			domain, addr),
		Detail:   err.Error(),
		Severity: SeverityError,
	}
}

func badRedirect(domain string, err error) Problem {
	return Problem{
		Name: "BadRedirect",
		Explanation: fmt.Sprintf(`Sending an ACME HTTP validation request to %s results in an unacceptable redirect. `+
			`This is most likely a misconfiguration of your web server or your web application.`,
			domain),
		Detail:   err.Error(),
		Severity: SeverityError,
	}
}
