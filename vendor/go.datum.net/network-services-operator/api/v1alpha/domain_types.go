// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Domain represents a domain name in the Datum system
//
// +kubebuilder:printcolumn:name="Domain Name",type="string",JSONPath=".spec.domainName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Verified",type="string",JSONPath=`.status.conditions[?(@.type=="Verified")].status`
// +kubebuilder:printcolumn:name="Verification Message",type="string",JSONPath=`.status.conditions[?(@.type=="Verified")].message`,priority=1
type Domain struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec DomainSpec `json:"spec,omitempty"`

	// +kubebuilder:default={conditions: {{type: "Verified", status: "Unknown", reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type: "VerifiedDNS", status: "Unknown", reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type: "VerifiedHTTP", status: "Unknown", reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}}
	Status DomainStatus `json:"status,omitempty"`
}

// DomainSpec defines the desired state of Domain
type DomainSpec struct {
	// DomainName is the fully qualified domain name (FQDN) to be managed
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:XValidation:message="A domain name is immutable and cannot be changed after creation",rule="oldSelf == '' || self == oldSelf"
	// +kubebuilder:validation:XValidation:message="Must have at least two segments separated by dots",rule="self.indexOf('.') != -1"
	DomainName string `json:"domainName"`

	// DesiredRegistrationRefreshAttempt is the desired time of the next registration refresh attempt.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:message="must be at least 5m after the previous desiredRegistrationRefreshAttempt when changed",rule="oldSelf == null || self == null || self == oldSelf || self >= oldSelf + duration('5m')"
	DesiredRegistrationRefreshAttempt *metav1.Time `json:"desiredRegistrationRefreshAttempt,omitempty"`
}

// DomainStatus defines the observed state of Domain
type DomainStatus struct {
	Verification *DomainVerificationStatus `json:"verification,omitempty"`
	Registration *Registration             `json:"registration,omitempty"`
	// Nameservers lists the authoritative NS for the *effective* domain name:
	// - If Apex == true: taken from RDAP for the registered domain (eTLD+1)
	// - If Apex == false: taken from DNS delegation for the subdomain; falls back to apex NS if no cut
	Nameservers []Nameserver `json:"nameservers,omitempty"`
	// Apex is true when spec.domainName is the registered domain (eTLD+1).
	Apex       bool               `json:"apex,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	// This condition is true when Domain ownership has been verified via either
	// DNS or HTTP.
	DomainConditionVerified = "Verified"

	// DomainConditionValidDomain indicates whether the provided domain name is
	// registrable (i.e., a valid eTLD+1) and suitable for verification/registration.
	DomainConditionValidDomain = "ValidDomain"

	// This condition tracks verification attempts via DNS.
	DomainConditionVerifiedDNS = "VerifiedDNS"

	// This condition tracks verification attempts via HTTP.
	DomainConditionVerifiedHTTP = "VerifiedHTTP"

	// This condition tracks verification attempts via managed DNS (DNSZone).
	DomainConditionVerifiedDNSZone = "VerifiedDNSZone"
)

const (
	// DomainReasonPendingVerification indicates domain verification is in
	// progress
	DomainReasonPendingVerification = "PendingVerification"

	// DomainReasonVerificationRecordContentMismatch indicates the
	// verification record content does not match expected values
	DomainReasonVerificationRecordContentMismatch = "VerificationRecordContentMismatch"

	// DomainReasonVerificationRecordNotFound indicates the verification record
	// was not found
	DomainReasonVerificationRecordNotFound = "RecordNotFound"

	// DomainReasonVerificationUnexpectedResponse indicates that an unexpected
	// response was encountered during verification.
	DomainReasonVerificationUnexpectedResponse = "UnexpectedResponse"

	// DomainReasonVerificationInternalError indicates that an internal error
	// was encountered during verification.
	DomainReasonVerificationInternalError = "InternalError"

	// DomainReasonVerified indicates domain ownership has been successfully
	// verified
	DomainReasonVerified = "Verified"

	// DomainReasonDNSZoneNotFound indicates there is no DNSZone referencing this Domain.
	DomainReasonDNSZoneNotFound = "DNSZoneNotFound"

	// DomainReasonDNSZoneNotReady indicates the DNSZone exists but is not Accepted/Programmed yet.
	DomainReasonDNSZoneNotReady = "DNSZoneNotReady"

	// DomainReasonDNSZoneNameserverMismatch indicates no overlap between the Domain's
	// provisioned nameservers and the DNSZone nameservers.
	DomainReasonDNSZoneNameserverMismatch = "DNSZoneNameserverMismatch"

	// DomainReasonInvalidDomain indicates the provided domain name is not
	// registrable (e.g., only a public suffix like "com"), and flows are paused.
	DomainReasonInvalidDomain = "InvalidApex"

	// DomainReasonValid indicates the provided domain name is registrable.
	DomainReasonValid = "Valid"
)

// DomainVerificationStatus represents the verification status of a domain
type DomainVerificationStatus struct {
	DNSRecord               DNSVerificationRecord `json:"dnsRecord,omitempty"`
	HTTPToken               HTTPVerificationToken `json:"httpToken,omitempty"`
	NextVerificationAttempt metav1.Time           `json:"nextVerificationAttempt,omitempty"`
}

// DNSVerificationRecord represents a DNS record required for verification
type DNSVerificationRecord struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type HTTPVerificationToken struct {
	URL  string `json:"url"`
	Body string `json:"body"`
}

// Registration represents the registration information for a domain
type Registration struct {
	// Identity & provenance
	Domain           string `json:"domain,omitempty"`           // FQDN as observed (punycode)
	RegistryDomainID string `json:"registryDomainID,omitempty"` // e.g., "12345-EXAMPLE"
	Handle           string `json:"handle,omitempty"`           // RDAP handle if present
	Source           string `json:"source,omitempty"`           // "rdap" | "whois"

	Registrar *RegistrarInfo `json:"registrar,omitempty"`
	Registry  *RegistryInfo  `json:"registry,omitempty"`

	// Lifecycle
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// Raw statuses that will either be rdap rfc8056 or whois EPP status strings
	Statuses []string `json:"statuses,omitempty"` // e.g., clientTransferProhibited (EPP) or client transfer prohibited (RDAP)

	// DNSSEC (from RDAP secureDNS, with WHOIS fallback when parsable)
	DNSSEC *DNSSECInfo `json:"dnssec,omitempty"`

	// Contacts (minimal, non-PII summary if available)
	Contacts *ContactSet `json:"contacts,omitempty"`

	// Abuse / support contacts (registrar/registry)
	Abuse *AbuseContact `json:"abuse,omitempty"`

	NextRefreshAttempt metav1.Time `json:"nextRefreshAttempt,omitempty"`
	LastRefreshAttempt metav1.Time `json:"lastRefreshAttempt,omitempty"`
}

type RegistrarInfo struct {
	IANAID string `json:"ianaID,omitempty"` // registrar IANA ID if known
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
}

type RegistryInfo struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

type Nameserver struct {
	Hostname string         `json:"hostname"`
	IPs      []NameserverIP `json:"ips,omitempty"`
}

// NameserverIP captures per-address provenance for a nameserver.
type NameserverIP struct {
	Address        string `json:"address"`                  // e.g., "192.0.2.10" or "2001:db8::1"
	RegistrantName string `json:"registrantName,omitempty"` // org/name from IP RDAP if available
}

type DNSSECInfo struct {
	Enabled *bool      `json:"enabled,omitempty"` // true if RDAP secureDNS/WHOIS indicates DNSSEC
	DS      []DSRecord `json:"ds,omitempty"`      // optional if RDAP provides dsData
}

type DSRecord struct {
	KeyTag     uint16 `json:"keyTag"`
	Algorithm  uint8  `json:"algorithm"`
	DigestType uint8  `json:"digestType"`
	Digest     string `json:"digest"`
}

type ContactSet struct {
	Registrant *Contact `json:"registrant,omitempty"`
	Admin      *Contact `json:"admin,omitempty"`
	Tech       *Contact `json:"tech,omitempty"`
}

type Contact struct {
	Organization string `json:"organization,omitempty"`
	Email        string `json:"email,omitempty"` // may be redacted
	Phone        string `json:"phone,omitempty"` // may be redacted
}

type AbuseContact struct {
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// +kubebuilder:object:root=true

// DomainList contains a list of Domain
type DomainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Domain `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Domain{}, &DomainList{})
}
