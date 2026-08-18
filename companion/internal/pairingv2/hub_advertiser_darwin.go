//go:build darwin

package pairingv2

import (
	"errors"
	"math/bits"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/go-webgpu/goffi/ffi"
	"github.com/go-webgpu/goffi/types"
	"golang.org/x/sys/unix"
)

const (
	dnsServiceFlagsAdd          = uint32(0x2)
	dnsServiceFlagsNoAutoRename = uint32(0x8)
	dnsServiceNoError           = int32(0)
	darwinRegistrationTimeout   = 3 * time.Second
	darwinPollInterval          = 100 * time.Millisecond
	darwinDNSServiceLibrary     = "/usr/lib/libSystem.B.dylib"
)

type darwinDNSServiceAPI struct {
	registerFunction      unsafe.Pointer
	processResultFunction unsafe.Pointer
	refSockFDFunction     unsafe.Pointer
	deallocateFunction    unsafe.Pointer
	registerCall          types.CallInterface
	processResultCall     types.CallInterface
	refSockFDCall         types.CallInterface
	deallocateCall        types.CallInterface
}

type darwinRegistrationEvent struct {
	flags     uint32
	errorCode int32
}

type darwinHubAdvertisement struct {
	api       *darwinDNSServiceAPI
	ref       uintptr
	contextID uintptr
	ready     chan error
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	healthy   atomic.Bool
}

var (
	darwinDNSServiceOnce sync.Once
	darwinDNSService     *darwinDNSServiceAPI
	darwinDNSServiceErr  error

	darwinCallbackOnce sync.Once
	darwinCallback     uintptr

	darwinRegistrationSequence atomic.Uint64
	darwinRegistrationMutex    sync.Mutex
	darwinRegistrations        = make(map[uintptr]chan<- darwinRegistrationEvent)

	darwinWaitReadable = waitForDarwinDNSService
)

func startPlatformHubAdvertisement(
	instance string,
	port int,
	networkInterface *net.Interface,
	_ netip.Addr,
) (hubAdvertisementBackend, error) {
	api, err := loadDarwinDNSServiceAPI()
	if err != nil {
		return nil, errors.New("load macOS Bonjour service")
	}
	if networkInterface == nil || networkInterface.Index <= 0 {
		return nil, errors.New("invalid Pairing v2 Hub interface")
	}

	contextID := uintptr(darwinRegistrationSequence.Add(1))
	if contextID == 0 {
		contextID = uintptr(darwinRegistrationSequence.Add(1))
	}
	events := make(chan darwinRegistrationEvent, 4)
	darwinRegistrationMutex.Lock()
	darwinRegistrations[contextID] = events
	darwinRegistrationMutex.Unlock()
	removeContext := true
	defer func() {
		if removeContext {
			darwinRegistrationMutex.Lock()
			delete(darwinRegistrations, contextID)
			darwinRegistrationMutex.Unlock()
		}
	}()

	textRecord := []byte{4, 'p', 'v', '=', '2'}
	var reference uintptr
	result, err := api.register(
		&reference,
		dnsServiceFlagsNoAutoRename,
		uint32(networkInterface.Index),
		instance,
		HubService,
		PairingDomain+".",
		bits.ReverseBytes16(uint16(port)),
		textRecord,
		darwinDNSServiceCallback(),
		contextID,
	)
	if err != nil || result != dnsServiceNoError || reference == 0 {
		return nil, errors.New("register Pairing v2 Hub with macOS Bonjour")
	}
	fileDescriptor, err := api.refSockFD(reference)
	if err != nil || fileDescriptor < 0 {
		_ = api.deallocate(reference)
		return nil, errors.New("open macOS Bonjour event source")
	}

	advertisement := &darwinHubAdvertisement{
		api: api, ref: reference, contextID: contextID,
		ready: make(chan error, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	removeContext = false
	go advertisement.run(events, fileDescriptor)

	timer := time.NewTimer(darwinRegistrationTimeout)
	defer timer.Stop()
	select {
	case readyErr := <-advertisement.ready:
		if readyErr != nil {
			_ = advertisement.Close()
			return nil, readyErr
		}
		return advertisement, nil
	case <-timer.C:
		_ = advertisement.Close()
		return nil, errors.New("macOS Bonjour did not confirm Pairing v2 Hub registration")
	}
}

func (advertisement *darwinHubAdvertisement) run(
	events <-chan darwinRegistrationEvent,
	fileDescriptor int32,
) {
	registered := false
	defer func() {
		advertisement.healthy.Store(false)
		_ = advertisement.api.deallocate(advertisement.ref)
		darwinRegistrationMutex.Lock()
		delete(darwinRegistrations, advertisement.contextID)
		darwinRegistrationMutex.Unlock()
		if !registered {
			advertisement.signalReady(errors.New("macOS Bonjour registration stopped"))
		}
		close(advertisement.done)
	}()

	for {
		select {
		case <-advertisement.stop:
			return
		default:
		}
		readable, err := darwinWaitReadable(fileDescriptor, darwinPollInterval)
		if err != nil {
			advertisement.signalReady(errors.New("wait for macOS Bonjour registration"))
			return
		}
		if !readable {
			continue
		}
		result, err := advertisement.api.processResult(advertisement.ref)
		if err != nil || result != dnsServiceNoError {
			advertisement.signalReady(errors.New("process macOS Bonjour registration"))
			return
		}
		for {
			select {
			case event := <-events:
				if event.errorCode != dnsServiceNoError || event.flags&dnsServiceFlagsAdd == 0 {
					advertisement.signalReady(errors.New("macOS Bonjour rejected Pairing v2 Hub registration"))
					return
				}
				if !registered {
					registered = true
					advertisement.healthy.Store(true)
					advertisement.signalReady(nil)
				}
			default:
				goto eventsDrained
			}
		}
	eventsDrained:
	}
}

func (advertisement *darwinHubAdvertisement) signalReady(err error) {
	select {
	case advertisement.ready <- err:
	default:
	}
}

func (advertisement *darwinHubAdvertisement) Close() error {
	if advertisement == nil {
		return nil
	}
	advertisement.closeOnce.Do(func() { close(advertisement.stop) })
	select {
	case <-advertisement.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("stop macOS Bonjour Pairing v2 Hub registration")
	}
}

func (advertisement *darwinHubAdvertisement) Healthy() bool {
	return advertisement != nil && advertisement.healthy.Load()
}

func loadDarwinDNSServiceAPI() (*darwinDNSServiceAPI, error) {
	darwinDNSServiceOnce.Do(func() {
		handle, err := ffi.LoadLibrary(darwinDNSServiceLibrary)
		if err != nil {
			darwinDNSServiceErr = err
			return
		}
		api := &darwinDNSServiceAPI{}
		symbols := []struct {
			name   string
			target *unsafe.Pointer
		}{
			{"DNSServiceRegister", &api.registerFunction},
			{"DNSServiceProcessResult", &api.processResultFunction},
			{"DNSServiceRefSockFD", &api.refSockFDFunction},
			{"DNSServiceRefDeallocate", &api.deallocateFunction},
		}
		for _, symbol := range symbols {
			*symbol.target, err = ffi.GetSymbol(handle, symbol.name)
			if err != nil {
				darwinDNSServiceErr = err
				return
			}
		}
		if err = ffi.PrepareCallInterface(
			&api.registerCall,
			types.DefaultCall,
			types.SInt32TypeDescriptor,
			[]*types.TypeDescriptor{
				types.PointerTypeDescriptor,
				types.UInt32TypeDescriptor,
				types.UInt32TypeDescriptor,
				types.PointerTypeDescriptor,
				types.PointerTypeDescriptor,
				types.PointerTypeDescriptor,
				types.PointerTypeDescriptor,
				types.UInt16TypeDescriptor,
				types.UInt16TypeDescriptor,
				types.PointerTypeDescriptor,
				types.PointerTypeDescriptor,
				types.PointerTypeDescriptor,
			},
		); err != nil {
			darwinDNSServiceErr = err
			return
		}
		if err = prepareDarwinDNSCall(&api.processResultCall, types.SInt32TypeDescriptor); err != nil {
			darwinDNSServiceErr = err
			return
		}
		if err = prepareDarwinDNSCall(&api.refSockFDCall, types.SInt32TypeDescriptor); err != nil {
			darwinDNSServiceErr = err
			return
		}
		if err = prepareDarwinDNSCall(&api.deallocateCall, types.VoidTypeDescriptor); err != nil {
			darwinDNSServiceErr = err
			return
		}
		darwinDNSService = api
	})
	return darwinDNSService, darwinDNSServiceErr
}

func prepareDarwinDNSCall(
	callInterface *types.CallInterface,
	returnType *types.TypeDescriptor,
) error {
	return ffi.PrepareCallInterface(
		callInterface,
		types.DefaultCall,
		returnType,
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	)
}

func (api *darwinDNSServiceAPI) register(
	reference *uintptr,
	flags uint32,
	interfaceIndex uint32,
	instance string,
	serviceType string,
	domain string,
	port uint16,
	textRecord []byte,
	callback uintptr,
	contextID uintptr,
) (int32, error) {
	instanceBytes := append([]byte(instance), 0)
	serviceTypeBytes := append([]byte(serviceType), 0)
	domainBytes := append([]byte(domain), 0)
	referenceTarget := unsafe.Pointer(reference)
	instancePointer := unsafe.Pointer(&instanceBytes[0])
	serviceTypePointer := unsafe.Pointer(&serviceTypeBytes[0])
	domainPointer := unsafe.Pointer(&domainBytes[0])
	var hostPointer uintptr
	textRecordLength := uint16(len(textRecord))
	textRecordPointer := unsafe.Pointer(&textRecord[0])
	callbackPointer := callback
	contextPointer := contextID
	var result int32
	_, err := ffi.CallFunction(
		&api.registerCall,
		api.registerFunction,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{
			unsafe.Pointer(&referenceTarget),
			unsafe.Pointer(&flags),
			unsafe.Pointer(&interfaceIndex),
			unsafe.Pointer(&instancePointer),
			unsafe.Pointer(&serviceTypePointer),
			unsafe.Pointer(&domainPointer),
			unsafe.Pointer(&hostPointer),
			unsafe.Pointer(&port),
			unsafe.Pointer(&textRecordLength),
			unsafe.Pointer(&textRecordPointer),
			unsafe.Pointer(&callbackPointer),
			unsafe.Pointer(&contextPointer),
		},
	)
	runtime.KeepAlive(instanceBytes)
	runtime.KeepAlive(serviceTypeBytes)
	runtime.KeepAlive(domainBytes)
	runtime.KeepAlive(textRecord)
	return result, err
}

func (api *darwinDNSServiceAPI) processResult(reference uintptr) (int32, error) {
	return api.int32Result(api.processResultFunction, &api.processResultCall, reference)
}

func (api *darwinDNSServiceAPI) refSockFD(reference uintptr) (int32, error) {
	return api.int32Result(api.refSockFDFunction, &api.refSockFDCall, reference)
}

func (api *darwinDNSServiceAPI) int32Result(
	function unsafe.Pointer,
	callInterface *types.CallInterface,
	reference uintptr,
) (int32, error) {
	referencePointer := reference
	var result int32
	_, err := ffi.CallFunction(
		callInterface,
		function,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{unsafe.Pointer(&referencePointer)},
	)
	return result, err
}

func (api *darwinDNSServiceAPI) deallocate(reference uintptr) error {
	referencePointer := reference
	_, err := ffi.CallFunction(
		&api.deallocateCall,
		api.deallocateFunction,
		nil,
		[]unsafe.Pointer{unsafe.Pointer(&referencePointer)},
	)
	return err
}

func darwinDNSServiceCallback() uintptr {
	darwinCallbackOnce.Do(func() {
		darwinCallback = ffi.NewCallback(func(
			_ uintptr,
			flags uint32,
			errorCode int32,
			_ uintptr,
			_ uintptr,
			_ uintptr,
			contextID uintptr,
		) {
			darwinRegistrationMutex.Lock()
			events := darwinRegistrations[contextID]
			darwinRegistrationMutex.Unlock()
			if events == nil {
				return
			}
			select {
			case events <- darwinRegistrationEvent{flags: flags, errorCode: errorCode}:
			default:
			}
		})
	})
	return darwinCallback
}

func waitForDarwinDNSService(fileDescriptor int32, timeout time.Duration) (bool, error) {
	pollTimeout := int(timeout.Milliseconds())
	if pollTimeout < 1 {
		pollTimeout = 1
	}
	descriptors := []unix.PollFd{{Fd: fileDescriptor, Events: unix.POLLIN}}
	count, err := unix.Poll(descriptors, pollTimeout)
	if err != nil {
		if errors.Is(err, unix.EINTR) {
			return false, nil
		}
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if descriptors[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
		return false, errors.New("macOS Bonjour event source failed")
	}
	return descriptors[0].Revents&unix.POLLIN != 0, nil
}
