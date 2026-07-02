//go:build darwin

package kerberos

/*
#cgo darwin LDFLAGS: -framework GSS
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
// Include directly from GSS.framework, which carries no deprecation markers
// on the POSIX GSSAPI functions (unlike the compatibility shim at
// /usr/include/gssapi/gssapi.h which Apple has deprecated).
#include <GSS/gssapi.h>
#include <GSS/gssapi_krb5.h>
#include <GSS/gssapi_oid.h>

// Alias for the Kerberos V5 mechanism OID provided by the framework.
// GSS_KRB5_MECHANISM is declared in gssapi_oid.h as an extern symbol.
#define GSS_MECH_KRB5 GSS_KRB5_MECHANISM

// Get detailed error message from GSS status codes.
// Returns a malloc'd string that must be freed by caller.
char* gss_error_string(OM_uint32 major, OM_uint32 minor) {
	OM_uint32 msg_ctx = 0;
	OM_uint32 min_stat;
	gss_buffer_desc status_string;
	char *result = NULL;
	size_t total_len = 0;

	do {
		gss_display_status(&min_stat, major, GSS_C_GSS_CODE,
		                  GSS_C_NO_OID, &msg_ctx, &status_string);
		char *new_result = realloc(result, total_len + status_string.length + 20);
		if (!new_result) {
			free(result);
			gss_release_buffer(&min_stat, &status_string);
			return strdup("(out of memory)");
		}
		result = new_result;
		if (total_len == 0) {
			sprintf(result + total_len, "Major: %.*s",
			        (int)status_string.length, (char *)status_string.value);
		} else {
			sprintf(result + total_len, " %.*s",
			        (int)status_string.length, (char *)status_string.value);
		}
		total_len = strlen(result);
		gss_release_buffer(&min_stat, &status_string);
	} while (msg_ctx != 0);

	if (minor != 0) {
		msg_ctx = 0;
		do {
			gss_display_status(&min_stat, minor, GSS_C_MECH_CODE,
			                  GSS_C_NO_OID, &msg_ctx, &status_string);
			char *new_result = realloc(result, total_len + status_string.length + 20);
			if (!new_result) {
				free(result);
				gss_release_buffer(&min_stat, &status_string);
				return strdup("(out of memory)");
			}
			result = new_result;
			sprintf(result + total_len, "; Minor: %.*s",
			        (int)status_string.length, (char *)status_string.value);
			total_len = strlen(result);
			gss_release_buffer(&min_stat, &status_string);
		} while (msg_ctx != 0);
	}

	return result ? result : strdup("(unknown error)");
}
*/
import "C"

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"unsafe"
)

var (
	available bool
	initMutex sync.Mutex
)

// Init initializes the GSSAPI subsystem on macOS.
// GSS.framework is always available on macOS, so this just sets the available flag.
func Init() error {
	initMutex.Lock()
	defer initMutex.Unlock()

	if available {
		return nil
	}

	available = true
	return nil
}

// Close is a no-op on macOS since GSS.framework doesn't need to be unloaded.
func Close() {
	initMutex.Lock()
	defer initMutex.Unlock()
	available = false
}

// Available returns true if GSSAPI is available (always true on macOS after Init).
func Available() bool {
	initMutex.Lock()
	defer initMutex.Unlock()
	return available
}

// GetSPNEGOToken acquires Kerberos credentials from the system's default
// credential cache and generates a Kerberos token for the given service
// principal name. Returns the base64-encoded token string.
//
// On macOS, this uses pure Kerberos V5 mechanism instead of SPNEGO because
// GSS.framework (Heimdal) does not properly support SPNEGO negotiation.
// Most servers accept pure Kerberos tokens via HTTP Negotiate authentication.
//
// The spn parameter should be in the format "HTTP@hostname" (not "HTTP/hostname").
//
//nolint:gocritic // dupSubExpr false positives from CGo GSS-API macros
func GetSPNEGOToken(spn string) (string, error) {
	initMutex.Lock()
	avail := available
	initMutex.Unlock()

	if !avail {
		return "", fmt.Errorf("GSSAPI not initialized")
	}

	var minorStatus C.OM_uint32
	var majorStatus C.OM_uint32

	spnCStr := C.CString(spn)
	defer C.free(unsafe.Pointer(spnCStr))

	var inputNameBuffer C.gss_buffer_desc
	inputNameBuffer.length = C.size_t(len(spn))
	inputNameBuffer.value = unsafe.Pointer(spnCStr)

	var serviceName C.gss_name_t
	majorStatus = C.gss_import_name(
		&minorStatus,
		&inputNameBuffer,
		C.GSS_C_NT_HOSTBASED_SERVICE,
		&serviceName,
	)
	if majorStatus != C.GSS_S_COMPLETE {
		return "", fmt.Errorf("gss_import_name failed: major=0x%x, minor=0x%x", majorStatus, minorStatus)
	}
	defer C.gss_release_name(&minorStatus, &serviceName)

	var cred C.gss_cred_id_t
	majorStatus = C.gss_acquire_cred(
		&minorStatus,
		C.GSS_C_NO_NAME,
		C.GSS_C_INDEFINITE,
		C.GSS_C_NO_OID_SET,
		C.GSS_C_INITIATE,
		&cred,
		nil,
		nil,
	)
	if majorStatus != C.GSS_S_COMPLETE {
		return "", fmt.Errorf("gss_acquire_cred failed: major=0x%x, minor=0x%x", majorStatus, minorStatus)
	}
	defer C.gss_release_cred(&minorStatus, &cred)

	var context C.gss_ctx_id_t = C.GSS_C_NO_CONTEXT
	var outputToken C.gss_buffer_desc
	var retFlags C.OM_uint32

	majorStatus = C.gss_init_sec_context(
		&minorStatus,
		cred,
		&context,
		serviceName,
		C.GSS_MECH_KRB5,
		C.GSS_C_MUTUAL_FLAG|C.GSS_C_REPLAY_FLAG|C.GSS_C_SEQUENCE_FLAG,
		0,
		C.GSS_C_NO_CHANNEL_BINDINGS,
		C.GSS_C_NO_BUFFER,
		nil,
		&outputToken,
		&retFlags,
		nil,
	)

	if majorStatus != C.GSS_S_COMPLETE && majorStatus != C.GSS_S_CONTINUE_NEEDED {
		if context != C.GSS_C_NO_CONTEXT {
			C.gss_delete_sec_context(&minorStatus, &context, C.GSS_C_NO_BUFFER)
		}
		errStr := C.gss_error_string(majorStatus, minorStatus)
		defer C.free(unsafe.Pointer(errStr))
		return "", fmt.Errorf("gss_init_sec_context failed: %s (major=0x%x, minor=0x%x)",
			C.GoString(errStr), majorStatus, minorStatus)
	}

	if context != C.GSS_C_NO_CONTEXT {
		defer C.gss_delete_sec_context(&minorStatus, &context, C.GSS_C_NO_BUFFER)
	}

	if outputToken.length == 0 {
		return "", fmt.Errorf("no token generated")
	}
	defer C.gss_release_buffer(&minorStatus, &outputToken)

	// C.GoBytes copies from C heap to Go heap before defers release the buffer.
	tokenBytes := C.GoBytes(outputToken.value, C.int(outputToken.length))
	return base64.StdEncoding.EncodeToString(tokenBytes), nil
}

// SetSPNEGOHeader generates a Kerberos token and sets it as an
// "Authorization: Negotiate <token>" header on the request.
func SetSPNEGOHeader(req *http.Request, spn string) error {
	token, err := GetSPNEGOToken(spn)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Negotiate "+token)
	return nil
}
