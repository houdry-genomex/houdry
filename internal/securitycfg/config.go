package securitycfg

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type duration time.Duration

func (d *duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err == nil && s != "" {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = duration(parsed)
		return nil
	}
	var n int64
	if err := value.Decode(&n); err != nil {
		return err
	}
	*d = duration(n)
	return nil
}

type Config struct {
	Security struct {
		MTLS      bool   `yaml:"mtls"`
		TLSListen string `yaml:"tls_listen"`
		Enrollment struct {
			TokenTTL    duration `yaml:"token_ttl"`
			MaxAttempts int      `yaml:"max_attempts"`
		} `yaml:"enrollment"`
		Certificates struct {
			Lifetime    duration `yaml:"lifetime"`
			RenewBefore duration `yaml:"renew_before"`
		} `yaml:"certificates"`
	} `yaml:"security"`
}

func (c Config) TokenTTL() time.Duration {
	return time.Duration(c.Security.Enrollment.TokenTTL)
}

func (c Config) MaxAttempts() int {
	return c.Security.Enrollment.MaxAttempts
}

func (c Config) CertLifetime() time.Duration {
	return time.Duration(c.Security.Certificates.Lifetime)
}

func (c Config) RenewBefore() time.Duration {
	return time.Duration(c.Security.Certificates.RenewBefore)
}

func Defaults() Config {
	var c Config
	c.Security.MTLS = true
	c.Security.Enrollment.TokenTTL = duration(10 * time.Minute)
	c.Security.Enrollment.MaxAttempts = 5
	c.Security.Certificates.Lifetime = duration(30 * 24 * time.Hour)
	c.Security.Certificates.RenewBefore = duration(7 * 24 * time.Hour)
	return c
}

func Load(path string) (Config, error) {
	if env := os.Getenv("HOODRY_SECURITY"); env != "" {
		path = env
	}
	c := Defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	d := Defaults()
	if c.Security.Enrollment.TokenTTL <= 0 {
		c.Security.Enrollment.TokenTTL = d.Security.Enrollment.TokenTTL
	}
	if c.Security.Enrollment.MaxAttempts <= 0 {
		c.Security.Enrollment.MaxAttempts = d.Security.Enrollment.MaxAttempts
	}
	if c.Security.Certificates.Lifetime <= 0 {
		c.Security.Certificates.Lifetime = d.Security.Certificates.Lifetime
	}
	if c.Security.Certificates.RenewBefore <= 0 {
		c.Security.Certificates.RenewBefore = d.Security.Certificates.RenewBefore
	}
	return c, nil
}
