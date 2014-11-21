package balancer

import (
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/getlantern/withtimeout"
)

// Dialer captures the configuration for dialing arbitrary addresses.
type Dialer struct {
	// Weight: determines how often this Dialer is used relative to the other
	// Dialers on the balancer.
	Weight int

	// QOS: identifies the quality of service provided by this dialer. Higher
	// numbers equal higher quality. "Quality" in this case is loosely defined,
	// but can mean things such as reliability, speed, etc.
	QOS int

	// Dial: this function dials the given network, addr.
	Dial func(network, addr string) (net.Conn, error)

	// Check: (optional) - When dialing fails, this Dialer is deactivated (taken
	// out of rotation). Check is a function that's used to periodically check
	// whether or not Dial works. As soon as there is a successful check, this
	// Dialer will be activated (put back in rotation).
	//
	// If Check is not specified, a default Check will be used that makes an
	// HTTP request to http://www.google.com/humans.txt using this Dialer.
	//
	// Checks are scheduled at exponentially increasing intervals that are
	// capped at 1 minute.
	Check func() bool
}

var (
	longDuration    = 1000000 * time.Hour
	maxCheckTimeout = 1 * time.Minute
)

type dialer struct {
	*Dialer
	active int32
	errCh  chan error
}

func (d *dialer) start() {
	d.active = 1
	d.errCh = make(chan error, 1000)
	if d.Check == nil {
		d.Check = d.defaultCheck
	}

	go func() {
		consecFailures := 0
		timer := time.NewTimer(longDuration)

		failed := func() {
			atomic.StoreInt32(&d.active, 0)
			consecFailures += 1
			timeout := time.Duration(consecFailures*consecFailures) * 100 * time.Millisecond
			if timeout > maxCheckTimeout {
				timeout = maxCheckTimeout
			}
			timer.Reset(timeout)
		}

		succeeded := func() {
			atomic.StoreInt32(&d.active, 1)
			consecFailures = 0
			timer.Reset(longDuration)
		}

		for {
			select {
			case _, ok := <-d.errCh:
				if !ok {
					log.Trace("dialer stopped")
					return
				}
				failed()
			case <-timer.C:
				ok := d.Check()
				if ok {
					succeeded()
				} else {
					failed()
				}
			}
		}
	}()
}

func (d *dialer) isactive() bool {
	return atomic.LoadInt32(&d.active) == 1
}

func (d *dialer) onError(err error) {
	d.errCh <- err
}

func (d *dialer) stop() {
	close(d.errCh)
}

func (d *dialer) defaultCheck() bool {
	client := &http.Client{
		Transport: &http.Transport{
			Dial: d.Dial,
		},
	}
	ok, timedOut, _ := withtimeout.Do(10*time.Second, func() (interface{}, error) {
		resp, err := client.Get("http://www.google.com/humans.txt")
		if err != nil {
			log.Tracef("Error on testing humans.txt: %s", err)
			return false, nil
		}
		resp.Body.Close()
		return resp.StatusCode == 200, nil
	})
	return !timedOut && ok.(bool)
}
