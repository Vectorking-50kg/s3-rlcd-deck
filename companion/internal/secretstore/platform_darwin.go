//go:build darwin

package secretstore

import (
	"bytes"
	"context"
	"errors"
	goruntime "runtime"
	"sync"
	"unsafe"

	"github.com/go-webgpu/goffi/ffi"
	"github.com/go-webgpu/goffi/types"
)

const (
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	securityPath       = "/System/Library/Frameworks/Security.framework/Security"
	cfStringUTF8       = uint32(0x08000100)

	errSecSuccess               = int32(0)
	errSecParam                 = int32(-50)
	errSecAllocate              = int32(-108)
	errSecUserCanceled          = int32(-128)
	errSecNotAvailable          = int32(-25291)
	errSecReadOnly              = int32(-25292)
	errSecAuthFailed            = int32(-25293)
	errSecNoSuchKeychain        = int32(-25294)
	errSecDuplicateItem         = int32(-25299)
	errSecItemNotFound          = int32(-25300)
	errSecInteractionNotAllowed = int32(-25308)
	errSecDecode                = int32(-26275)
	errSecMissingEntitlement    = int32(-34018)
)

type darwinRuntime struct {
	secItemAdd          unsafe.Pointer
	secItemUpdate       unsafe.Pointer
	secItemCopyMatching unsafe.Pointer
	secItemDelete       unsafe.Pointer

	cfStringCreateWithBytes unsafe.Pointer
	cfDataCreate            unsafe.Pointer
	cfDictionaryCreate      unsafe.Pointer
	cfRelease               unsafe.Pointer
	cfDataGetLength         unsafe.Pointer
	cfDataGetBytePtr        unsafe.Pointer
	cfArrayGetCount         unsafe.Pointer
	cfArrayGetValueAtIndex  unsafe.Pointer
	cfDictionaryGetValue    unsafe.Pointer
	cfStringGetLength       unsafe.Pointer
	cfStringGetCString      unsafe.Pointer

	secClass                   unsafe.Pointer
	secClassGenericPassword    unsafe.Pointer
	secAttrService             unsafe.Pointer
	secAttrAccount             unsafe.Pointer
	secValueData               unsafe.Pointer
	secReturnData              unsafe.Pointer
	secReturnAttributes        unsafe.Pointer
	secMatchLimit              unsafe.Pointer
	secMatchLimitOne           unsafe.Pointer
	secMatchLimitAll           unsafe.Pointer
	secUseAuthenticationUI     unsafe.Pointer
	secUseAuthenticationUIFail unsafe.Pointer
	cfBooleanTrue              unsafe.Pointer
	dictionaryKeyCallbacks     unsafe.Pointer
	dictionaryValueCallbacks   unsafe.Pointer
}

type darwinVault struct {
	runtime *darwinRuntime
	service string
}

var (
	darwinRuntimeOnce        sync.Once
	sharedDarwinRuntime      *darwinRuntime
	sharedDarwinRuntimeError error
)

func platformVault(service string) (vault, error) {
	if service == "" {
		return nil, ErrInvalid
	}
	darwinRuntimeOnce.Do(func() {
		sharedDarwinRuntime, sharedDarwinRuntimeError = loadDarwinRuntime()
	})
	if sharedDarwinRuntimeError != nil {
		return nil, ErrUnavailable
	}
	return &darwinVault{runtime: sharedDarwinRuntime, service: service}, nil
}

func (vault *darwinVault) Create(ctx context.Context, account string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	arena := newCFArena(vault.runtime)
	defer arena.release()
	keys, values, err := vault.baseAttributes(arena, account, true)
	if err != nil {
		return err
	}
	value, err := arena.data(secret)
	if err != nil {
		return err
	}
	keys = append(keys, vault.runtime.secValueData)
	values = append(values, value)
	attributes, err := arena.dictionary(keys, values)
	if err != nil {
		return err
	}
	status, err := vault.runtime.status(
		vault.runtime.secItemAdd,
		[]unsafe.Pointer{unsafe.Pointer(&attributes), pointerToNil()},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor, types.PointerTypeDescriptor},
	)
	if err != nil {
		return ErrUnavailable
	}
	return darwinVaultError(status)
}

func (vault *darwinVault) Update(ctx context.Context, account string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	arena := newCFArena(vault.runtime)
	defer arena.release()
	queryKeys, queryValues, err := vault.baseAttributes(arena, account, true)
	if err != nil {
		return err
	}
	query, err := arena.dictionary(queryKeys, queryValues)
	if err != nil {
		return err
	}
	value, err := arena.data(secret)
	if err != nil {
		return err
	}
	updates, err := arena.dictionary(
		[]unsafe.Pointer{vault.runtime.secValueData},
		[]unsafe.Pointer{value},
	)
	if err != nil {
		return err
	}
	status, err := vault.runtime.status(
		vault.runtime.secItemUpdate,
		[]unsafe.Pointer{unsafe.Pointer(&query), unsafe.Pointer(&updates)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor, types.PointerTypeDescriptor},
	)
	if err != nil {
		return ErrUnavailable
	}
	return darwinVaultError(status)
}

func (vault *darwinVault) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	arena := newCFArena(vault.runtime)
	defer arena.release()
	keys, values, err := vault.baseAttributes(arena, account, true)
	if err != nil {
		return nil, err
	}
	keys = append(keys, vault.runtime.secReturnData, vault.runtime.secMatchLimit)
	values = append(values, vault.runtime.cfBooleanTrue, vault.runtime.secMatchLimitOne)
	query, err := arena.dictionary(keys, values)
	if err != nil {
		return nil, err
	}
	var result unsafe.Pointer
	resultTarget := unsafe.Pointer(&result)
	status, err := vault.runtime.status(
		vault.runtime.secItemCopyMatching,
		[]unsafe.Pointer{unsafe.Pointer(&query), unsafe.Pointer(&resultTarget)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor, types.PointerTypeDescriptor},
	)
	if err != nil {
		return nil, ErrUnavailable
	}
	if mapped := darwinVaultError(status); mapped != nil {
		return nil, mapped
	}
	if result == nil {
		return nil, ErrCorrupt
	}
	arena.own(result)
	secret, err := vault.runtime.dataBytes(result)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		overwrite(secret)
		return nil, err
	}
	return secret, nil
}

func (vault *darwinVault) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	arena := newCFArena(vault.runtime)
	defer arena.release()
	keys, values, err := vault.baseAttributes(arena, account, true)
	if err != nil {
		return err
	}
	query, err := arena.dictionary(keys, values)
	if err != nil {
		return err
	}
	status, err := vault.runtime.status(
		vault.runtime.secItemDelete,
		[]unsafe.Pointer{unsafe.Pointer(&query)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	)
	if err != nil {
		return ErrUnavailable
	}
	return darwinVaultError(status)
}

func (vault *darwinVault) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	arena := newCFArena(vault.runtime)
	defer arena.release()
	service, err := arena.string(vault.service)
	if err != nil {
		return nil, err
	}
	keys := []unsafe.Pointer{
		vault.runtime.secClass,
		vault.runtime.secAttrService,
		vault.runtime.secReturnAttributes,
		vault.runtime.secMatchLimit,
		vault.runtime.secUseAuthenticationUI,
	}
	values := []unsafe.Pointer{
		vault.runtime.secClassGenericPassword,
		service,
		vault.runtime.cfBooleanTrue,
		vault.runtime.secMatchLimitAll,
		vault.runtime.secUseAuthenticationUIFail,
	}
	query, err := arena.dictionary(keys, values)
	if err != nil {
		return nil, err
	}
	var result unsafe.Pointer
	resultTarget := unsafe.Pointer(&result)
	status, err := vault.runtime.status(
		vault.runtime.secItemCopyMatching,
		[]unsafe.Pointer{unsafe.Pointer(&query), unsafe.Pointer(&resultTarget)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor, types.PointerTypeDescriptor},
	)
	if err != nil {
		return nil, ErrUnavailable
	}
	if status == errSecItemNotFound {
		return []string{}, nil
	}
	if mapped := darwinVaultError(status); mapped != nil {
		return nil, mapped
	}
	if result == nil {
		return nil, ErrCorrupt
	}
	arena.own(result)
	count, err := vault.runtime.arrayCount(result)
	if err != nil || count < 0 || count > maximumMetadata {
		return nil, ErrCorrupt
	}
	accounts := make([]string, 0, int(count))
	for index := int64(0); index < count; index++ {
		item, itemErr := vault.runtime.arrayValue(result, index)
		if itemErr != nil || item == nil {
			return nil, ErrCorrupt
		}
		accountValue, itemErr := vault.runtime.dictionaryValue(item, vault.runtime.secAttrAccount)
		if itemErr != nil || accountValue == nil {
			return nil, ErrCorrupt
		}
		account, itemErr := vault.runtime.stringValue(accountValue)
		if itemErr != nil {
			return nil, ErrCorrupt
		}
		accounts = append(accounts, account)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

func (vault *darwinVault) baseAttributes(
	arena *cfArena,
	account string,
	suppressAuthenticationUI bool,
) ([]unsafe.Pointer, []unsafe.Pointer, error) {
	service, err := arena.string(vault.service)
	if err != nil {
		return nil, nil, err
	}
	accountValue, err := arena.string(account)
	if err != nil {
		return nil, nil, err
	}
	keys := []unsafe.Pointer{
		vault.runtime.secClass,
		vault.runtime.secAttrService,
		vault.runtime.secAttrAccount,
	}
	values := []unsafe.Pointer{
		vault.runtime.secClassGenericPassword,
		service,
		accountValue,
	}
	if suppressAuthenticationUI {
		keys = append(keys, vault.runtime.secUseAuthenticationUI)
		values = append(values, vault.runtime.secUseAuthenticationUIFail)
	}
	return keys, values, nil
}

type cfArena struct {
	runtime *darwinRuntime
	owned   []unsafe.Pointer
}

func newCFArena(runtime *darwinRuntime) *cfArena {
	return &cfArena{runtime: runtime}
}

func (arena *cfArena) own(value unsafe.Pointer) unsafe.Pointer {
	if value != nil {
		arena.owned = append(arena.owned, value)
	}
	return value
}

func (arena *cfArena) release() {
	for index := len(arena.owned) - 1; index >= 0; index-- {
		_ = arena.runtime.release(arena.owned[index])
	}
	arena.owned = nil
}

func (arena *cfArena) string(value string) (unsafe.Pointer, error) {
	bytesValue := []byte(value)
	var bytesPointer unsafe.Pointer
	if len(bytesValue) != 0 {
		bytesPointer = unsafe.Pointer(&bytesValue[0])
	}
	allocator := unsafe.Pointer(nil)
	length := int64(len(bytesValue))
	encoding := cfStringUTF8
	external := uint8(0)
	var result unsafe.Pointer
	err := arena.runtime.call(
		arena.runtime.cfStringCreateWithBytes,
		types.PointerTypeDescriptor,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{
			unsafe.Pointer(&allocator), unsafe.Pointer(&bytesPointer), unsafe.Pointer(&length),
			unsafe.Pointer(&encoding), unsafe.Pointer(&external),
		},
		[]*types.TypeDescriptor{
			types.PointerTypeDescriptor, types.PointerTypeDescriptor, types.SInt64TypeDescriptor,
			types.UInt32TypeDescriptor, types.UInt8TypeDescriptor,
		},
	)
	goruntime.KeepAlive(bytesValue)
	if err != nil || result == nil {
		return nil, ErrUnavailable
	}
	return arena.own(result), nil
}

func (arena *cfArena) data(value []byte) (unsafe.Pointer, error) {
	if len(value) == 0 {
		return nil, ErrInvalid
	}
	allocator := unsafe.Pointer(nil)
	bytesPointer := unsafe.Pointer(&value[0])
	length := int64(len(value))
	var result unsafe.Pointer
	err := arena.runtime.call(
		arena.runtime.cfDataCreate,
		types.PointerTypeDescriptor,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{unsafe.Pointer(&allocator), unsafe.Pointer(&bytesPointer), unsafe.Pointer(&length)},
		[]*types.TypeDescriptor{
			types.PointerTypeDescriptor, types.PointerTypeDescriptor, types.SInt64TypeDescriptor,
		},
	)
	goruntime.KeepAlive(value)
	if err != nil || result == nil {
		return nil, ErrUnavailable
	}
	return arena.own(result), nil
}

func (arena *cfArena) dictionary(keys, values []unsafe.Pointer) (unsafe.Pointer, error) {
	if len(keys) == 0 || len(keys) != len(values) {
		return nil, ErrInvalid
	}
	allocator := unsafe.Pointer(nil)
	keysPointer := unsafe.Pointer(&keys[0])
	valuesPointer := unsafe.Pointer(&values[0])
	count := int64(len(keys))
	keyCallbacks := arena.runtime.dictionaryKeyCallbacks
	valueCallbacks := arena.runtime.dictionaryValueCallbacks
	var result unsafe.Pointer
	err := arena.runtime.call(
		arena.runtime.cfDictionaryCreate,
		types.PointerTypeDescriptor,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{
			unsafe.Pointer(&allocator), unsafe.Pointer(&keysPointer), unsafe.Pointer(&valuesPointer),
			unsafe.Pointer(&count), unsafe.Pointer(&keyCallbacks), unsafe.Pointer(&valueCallbacks),
		},
		[]*types.TypeDescriptor{
			types.PointerTypeDescriptor, types.PointerTypeDescriptor, types.PointerTypeDescriptor,
			types.SInt64TypeDescriptor, types.PointerTypeDescriptor, types.PointerTypeDescriptor,
		},
	)
	goruntime.KeepAlive(keys)
	goruntime.KeepAlive(values)
	if err != nil || result == nil {
		return nil, ErrUnavailable
	}
	return arena.own(result), nil
}

func (runtime *darwinRuntime) status(
	function unsafe.Pointer,
	arguments []unsafe.Pointer,
	argumentTypes []*types.TypeDescriptor,
) (int32, error) {
	var status int32
	err := runtime.call(
		function,
		types.SInt32TypeDescriptor,
		unsafe.Pointer(&status),
		arguments,
		argumentTypes,
	)
	return status, err
}

func (runtime *darwinRuntime) call(
	function unsafe.Pointer,
	returnType *types.TypeDescriptor,
	result unsafe.Pointer,
	arguments []unsafe.Pointer,
	argumentTypes []*types.TypeDescriptor,
) error {
	var callInterface types.CallInterface
	if err := ffi.PrepareCallInterface(
		&callInterface,
		types.DefaultCall,
		returnType,
		argumentTypes,
	); err != nil {
		return err
	}
	_, err := ffi.CallFunction(&callInterface, function, result, arguments)
	return err
}

func (runtime *darwinRuntime) release(value unsafe.Pointer) error {
	argument := value
	return runtime.call(
		runtime.cfRelease,
		types.VoidTypeDescriptor,
		nil,
		[]unsafe.Pointer{unsafe.Pointer(&argument)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	)
}

func (runtime *darwinRuntime) dataBytes(value unsafe.Pointer) ([]byte, error) {
	argument := value
	var length int64
	if err := runtime.call(
		runtime.cfDataGetLength,
		types.SInt64TypeDescriptor,
		unsafe.Pointer(&length),
		[]unsafe.Pointer{unsafe.Pointer(&argument)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	); err != nil || length <= 0 || length > maximumSecretBytes {
		return nil, ErrCorrupt
	}
	var pointer unsafe.Pointer
	if err := runtime.call(
		runtime.cfDataGetBytePtr,
		types.PointerTypeDescriptor,
		unsafe.Pointer(&pointer),
		[]unsafe.Pointer{unsafe.Pointer(&argument)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	); err != nil || pointer == nil {
		return nil, ErrCorrupt
	}
	return append([]byte(nil), unsafe.Slice((*byte)(pointer), int(length))...), nil
}

func (runtime *darwinRuntime) arrayCount(value unsafe.Pointer) (int64, error) {
	argument := value
	var count int64
	err := runtime.call(
		runtime.cfArrayGetCount,
		types.SInt64TypeDescriptor,
		unsafe.Pointer(&count),
		[]unsafe.Pointer{unsafe.Pointer(&argument)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	)
	return count, err
}

func (runtime *darwinRuntime) arrayValue(value unsafe.Pointer, index int64) (unsafe.Pointer, error) {
	argument := value
	var result unsafe.Pointer
	err := runtime.call(
		runtime.cfArrayGetValueAtIndex,
		types.PointerTypeDescriptor,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{unsafe.Pointer(&argument), unsafe.Pointer(&index)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor, types.SInt64TypeDescriptor},
	)
	return result, err
}

func (runtime *darwinRuntime) dictionaryValue(
	dictionary unsafe.Pointer,
	key unsafe.Pointer,
) (unsafe.Pointer, error) {
	var result unsafe.Pointer
	err := runtime.call(
		runtime.cfDictionaryGetValue,
		types.PointerTypeDescriptor,
		unsafe.Pointer(&result),
		[]unsafe.Pointer{unsafe.Pointer(&dictionary), unsafe.Pointer(&key)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor, types.PointerTypeDescriptor},
	)
	return result, err
}

func (runtime *darwinRuntime) stringValue(value unsafe.Pointer) (string, error) {
	argument := value
	var length int64
	if err := runtime.call(
		runtime.cfStringGetLength,
		types.SInt64TypeDescriptor,
		unsafe.Pointer(&length),
		[]unsafe.Pointer{unsafe.Pointer(&argument)},
		[]*types.TypeDescriptor{types.PointerTypeDescriptor},
	); err != nil || length <= 0 || length > 128 {
		return "", ErrCorrupt
	}
	buffer := make([]byte, length*4+1)
	bufferPointer := unsafe.Pointer(&buffer[0])
	bufferSize := int64(len(buffer))
	encoding := cfStringUTF8
	var converted uint8
	if err := runtime.call(
		runtime.cfStringGetCString,
		types.UInt8TypeDescriptor,
		unsafe.Pointer(&converted),
		[]unsafe.Pointer{
			unsafe.Pointer(&argument), unsafe.Pointer(&bufferPointer),
			unsafe.Pointer(&bufferSize), unsafe.Pointer(&encoding),
		},
		[]*types.TypeDescriptor{
			types.PointerTypeDescriptor, types.PointerTypeDescriptor,
			types.SInt64TypeDescriptor, types.UInt32TypeDescriptor,
		},
	); err != nil || converted == 0 {
		return "", ErrCorrupt
	}
	goruntime.KeepAlive(buffer)
	terminator := bytes.IndexByte(buffer, 0)
	// Secret References are ASCII, so UTF-8 bytes and UTF-16 code units must
	// match exactly. This also rejects embedded NUL and non-ASCII account names.
	if terminator <= 0 || int64(terminator) != length {
		return "", ErrCorrupt
	}
	return string(buffer[:terminator]), nil
}

func loadDarwinRuntime() (*darwinRuntime, error) {
	security, err := ffi.LoadLibrary(securityPath)
	if err != nil {
		return nil, err
	}
	coreFoundation, err := ffi.LoadLibrary(coreFoundationPath)
	if err != nil {
		return nil, err
	}
	runtime := &darwinRuntime{}
	functionSymbols := []struct {
		handle unsafe.Pointer
		name   string
		target *unsafe.Pointer
	}{
		{security, "SecItemAdd", &runtime.secItemAdd},
		{security, "SecItemUpdate", &runtime.secItemUpdate},
		{security, "SecItemCopyMatching", &runtime.secItemCopyMatching},
		{security, "SecItemDelete", &runtime.secItemDelete},
		{coreFoundation, "CFStringCreateWithBytes", &runtime.cfStringCreateWithBytes},
		{coreFoundation, "CFDataCreate", &runtime.cfDataCreate},
		{coreFoundation, "CFDictionaryCreate", &runtime.cfDictionaryCreate},
		{coreFoundation, "CFRelease", &runtime.cfRelease},
		{coreFoundation, "CFDataGetLength", &runtime.cfDataGetLength},
		{coreFoundation, "CFDataGetBytePtr", &runtime.cfDataGetBytePtr},
		{coreFoundation, "CFArrayGetCount", &runtime.cfArrayGetCount},
		{coreFoundation, "CFArrayGetValueAtIndex", &runtime.cfArrayGetValueAtIndex},
		{coreFoundation, "CFDictionaryGetValue", &runtime.cfDictionaryGetValue},
		{coreFoundation, "CFStringGetLength", &runtime.cfStringGetLength},
		{coreFoundation, "CFStringGetCString", &runtime.cfStringGetCString},
	}
	for _, symbol := range functionSymbols {
		*symbol.target, err = ffi.GetSymbol(symbol.handle, symbol.name)
		if err != nil {
			return nil, err
		}
	}
	valueSymbols := []struct {
		handle unsafe.Pointer
		name   string
		target *unsafe.Pointer
	}{
		{security, "kSecClass", &runtime.secClass},
		{security, "kSecClassGenericPassword", &runtime.secClassGenericPassword},
		{security, "kSecAttrService", &runtime.secAttrService},
		{security, "kSecAttrAccount", &runtime.secAttrAccount},
		{security, "kSecValueData", &runtime.secValueData},
		{security, "kSecReturnData", &runtime.secReturnData},
		{security, "kSecReturnAttributes", &runtime.secReturnAttributes},
		{security, "kSecMatchLimit", &runtime.secMatchLimit},
		{security, "kSecMatchLimitOne", &runtime.secMatchLimitOne},
		{security, "kSecMatchLimitAll", &runtime.secMatchLimitAll},
		{security, "kSecUseAuthenticationUI", &runtime.secUseAuthenticationUI},
		{security, "kSecUseAuthenticationUIFail", &runtime.secUseAuthenticationUIFail},
		{coreFoundation, "kCFBooleanTrue", &runtime.cfBooleanTrue},
	}
	for _, symbol := range valueSymbols {
		address, symbolErr := ffi.GetSymbol(symbol.handle, symbol.name)
		if symbolErr != nil || address == nil {
			return nil, errors.New("secret store runtime unavailable")
		}
		*symbol.target = *(*unsafe.Pointer)(address)
		if *symbol.target == nil {
			return nil, errors.New("secret store runtime unavailable")
		}
	}
	runtime.dictionaryKeyCallbacks, err = ffi.GetSymbol(
		coreFoundation,
		"kCFTypeDictionaryKeyCallBacks",
	)
	if err != nil {
		return nil, err
	}
	runtime.dictionaryValueCallbacks, err = ffi.GetSymbol(
		coreFoundation,
		"kCFTypeDictionaryValueCallBacks",
	)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

func pointerToNil() unsafe.Pointer {
	var value unsafe.Pointer
	return unsafe.Pointer(&value)
}

func darwinVaultError(status int32) error {
	switch status {
	case errSecSuccess:
		return nil
	case errSecDuplicateItem:
		return ErrDuplicate
	case errSecItemNotFound:
		return ErrNotFound
	case errSecInteractionNotAllowed:
		return ErrLocked
	case errSecUserCanceled:
		return ErrCanceled
	case errSecReadOnly, errSecAuthFailed, errSecMissingEntitlement:
		return ErrPermission
	case errSecParam:
		return ErrInvalid
	case errSecDecode:
		return ErrCorrupt
	case errSecAllocate, errSecNotAvailable, errSecNoSuchKeychain:
		return ErrUnavailable
	default:
		return ErrUnavailable
	}
}
