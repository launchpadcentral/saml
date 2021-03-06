// Package samlsp provides helpers that can be used to protect web
// services using SAML.
package samlsp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/launchpadcentral/saml"
	"github.com/launchpadcentral/saml/logger"
)

// Options represents the parameters for creating a new middleware
type Options struct {
	URL               url.URL
	Key               *rsa.PrivateKey
	Logger            logger.Interface
	Certificate       *x509.Certificate
	AllowIDPInitiated bool
	IDPMetadata       *saml.EntityDescriptor
	IDPMetadataURL    *url.URL
	HTTPClient        *http.Client
	CookieMaxAge      time.Duration
	ForceAuthn        bool
	RetryCount        int
}

// New creates a new Middleware
func New(opts Options) (*Middleware, error) {

	metadataURL := opts.URL
	metadataURL.Path = metadataURL.Path + "/saml/metadata"
	acsURL := opts.URL
	acsURL.Path = acsURL.Path + "/saml/acs"
	logr := opts.Logger
	if logr == nil {
		logr = logger.DefaultLogger
	}

	if opts.RetryCount == 0 {
		opts.RetryCount = 10
	}
	cookieMaxAge := opts.CookieMaxAge
	if opts.CookieMaxAge == 0 {
		cookieMaxAge = defaultCookieMaxAge
	}

	m := &Middleware{
		ServiceProvider: saml.ServiceProvider{
			Key:          opts.Key,
			Logger:       logr,
			Certificate:  opts.Certificate,
			MetadataURL:  metadataURL,
			AcsURL:       acsURL,
			IDPMetadata:  opts.IDPMetadata,
			ForceAuthn:   &opts.ForceAuthn,
			IDPMetadatas: map[string]saml.EntityDescriptor{},
		},
		AllowIDPInitiated: opts.AllowIDPInitiated,
		CookieName:        defaultCookieName,
		CookieMaxAge:      cookieMaxAge,
		CookieDomain:      opts.URL.Host,
		RetryCount:        opts.RetryCount,
	}

	// fetch the IDP metadata if needed.
	if opts.IDPMetadataURL == nil {
		return m, nil
	}

	if err := m.FetchIDPMetadata(opts.HTTPClient, opts.IDPMetadataURL); err != nil {
		return nil, err
	}

	return m, nil
}

// AddIDPMetadata adds metadata information do the IDPMetadatas map and uses the EntityID as the key value.
func (m *Middleware) AddIDPMetadata(metadata []byte) error {
	entity := &saml.EntityDescriptor{}
	err := xml.Unmarshal(metadata, entity)

	// this comparison is ugly, but it is how the error is generated in encoding/xml
	if err != nil && err.Error() == "expected element type <EntityDescriptor> but have <EntitiesDescriptor>" {
		entities := &saml.EntitiesDescriptor{}
		if err := xml.Unmarshal(metadata, entities); err != nil {
			return err
		}

		err = fmt.Errorf("no entity found with IDPSSODescriptor")
		for _, e := range entities.EntityDescriptors {
			if len(e.IDPSSODescriptors) > 0 {
				entity = &e
				err = nil
			}
		}
	}
	if err != nil {
		return err
	}

	// TODO keeping this only for making it backward compatible
	m.ServiceProvider.IDPMetadata = entity

	m.ServiceProvider.IDPMetadatas[entity.EntityID] = *entity

	return nil
}

// FetchIDPMetadata fetches the IdP Metadata from the given url.
func (m *Middleware) FetchIDPMetadata(c *http.Client, iDPMetadataURL *url.URL) error {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequest("GET", iDPMetadataURL.String(), nil)
	if err != nil {
		return err
	}
	// Some providers (like OneLogin) do not work properly unless the User-Agent header is specified.
	// Setting the user agent prevents the 403 Forbidden errors.
	req.Header.Set("User-Agent", "Golang; github.com/launchpadcentral/saml")

	for i := 0; true; i++ {
		resp, err := c.Do(req)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%d %s", resp.StatusCode, resp.Status)
		}
		var data []byte
		if err == nil {
			data, err = ioutil.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if err != nil {
			if i > m.RetryCount {
				return err
			}
			m.ServiceProvider.Logger.Printf("ERROR: %s: %s (will retry)", iDPMetadataURL, err)
			time.Sleep(5 * time.Second)
			continue
		}

		return m.AddIDPMetadata(data)
	}

	return errors.New("metadata fetch retry limit is reached")
}
