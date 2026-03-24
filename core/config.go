package core

import (
	"flag"
	"os/exec"
)

// Config holds the settings used to launch and connect to a browser instance.
type Config struct {
	UserDataDir           string
	Headless              bool
	BrowserExecutablePath string
	BrowserArgs           []string
	Sandbox               bool
	Lang                  string
	Host                  string
	Port                  int
	TempProfile           bool
}

// SetDefaults populates Config with default values.
func (c *Config) SetDefaults() {
	c.Sandbox = true
	c.Lang = "en-US,en;q=0.9"
	c.Headless = true
	c.TempProfile = true
}

// Option is a functional option that can be applied to a Config.
type Option interface {
	Apply(*Config) error
}

type optionFunc func(*Config) error

func (f optionFunc) Apply(c *Config) error {
	return f(c)
}

// WithUserDataDir sets the user profile data directory.
func WithUserDataDir(dir string) Option {
	return optionFunc(func(c *Config) error {
		c.UserDataDir = dir
		return nil
	})
}

// WithHeadless controls whether the browser starts in headless mode.
func WithHeadless(headless bool) Option {
	return optionFunc(func(c *Config) error {
		c.Headless = headless
		return nil
	})
}

// WithBrowserExecutableName resolves a browser binary by name from the system PATH.
func WithBrowserExecutableName(name string) Option {
	return optionFunc(func(c *Config) (err error) {
		c.BrowserExecutablePath, err = exec.LookPath(name)
		return err
	})
}

// WithBrowserExecutablePath sets an explicit path to the browser binary.
func WithBrowserExecutablePath(path string) Option {
	return optionFunc(func(c *Config) error {
		c.BrowserExecutablePath = path
		return nil
	})
}

// WithBrowserArgs appends additional command-line arguments for the browser process.
func WithBrowserArgs(args ...string) Option {
	return optionFunc(func(c *Config) error {
		c.BrowserArgs = append(c.BrowserArgs, args...)
		return nil
	})
}

// WithSandbox controls whether the browser's OS sandbox is enabled.
func WithSandbox(sandbox bool) Option {
	return optionFunc(func(c *Config) error {
		c.Sandbox = sandbox
		return nil
	})
}

// WithLang sets the language / accept-language string passed to the browser.
func WithLang(lang string) Option {
	return optionFunc(func(c *Config) error {
		c.Lang = lang
		return nil
	})
}

// WithHost sets the remote debugging host when connecting to an existing browser.
func WithHost(host string) Option {
	return optionFunc(func(c *Config) error {
		c.Host = host
		return nil
	})
}

// WithPort sets the remote debugging port. A non-zero value causes Start to
// connect to an already-running browser instead of launching a new one.
func WithPort(port int) Option {
	return optionFunc(func(c *Config) error {
		c.Port = port
		return nil
	})
}

// WithUseTempProfileDir controls whether a temporary profile directory is
// created when no explicit user data directory is provided.
func WithUseTempProfileDir(useTempProfileDir bool) Option {
	return optionFunc(func(c *Config) error {
		c.TempProfile = useTempProfileDir
		return nil
	})
}

// WithFlags registers browser configuration flags on the given FlagSet and
// returns an Option that applies any flags the user has set.
func WithFlags(fs *flag.FlagSet) Option {
	var config Config
	config.SetDefaults()
	fs.StringVar(&config.UserDataDir, "user-data-dir", config.UserDataDir, "User data directory")
	fs.BoolVar(&config.Headless, "headless", config.Headless, "Start in headless mode")
	fs.StringVar(&config.BrowserExecutablePath, "browser-executable-path", config.BrowserExecutablePath, "Path to browser executable")
	fs.BoolVar(&config.Sandbox, "sandbox", config.Sandbox, "Enable sandbox mode")
	fs.StringVar(&config.Lang, "lang", config.Lang, "Language string to use")
	fs.StringVar(&config.Host, "host", config.Host, "Remote debugging host")
	fs.IntVar(&config.Port, "port", config.Port, "Remote debugging port")
	fs.BoolVar(&config.TempProfile, "temp-profile", config.TempProfile, "Use a temporary profile directory if none is specified")

	return optionFunc(func(c *Config) error {
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "user-data-dir":
				c.UserDataDir = config.UserDataDir
			case "headless":
				c.Headless = config.Headless
			case "browser-executable-path":
				c.BrowserExecutablePath = config.BrowserExecutablePath
			case "sandbox":
				c.Sandbox = config.Sandbox
			case "lang":
				c.Lang = config.Lang
			case "host":
				c.Host = config.Host
			case "port":
				c.Port = config.Port
			case "temp-profile":
				c.TempProfile = config.TempProfile
			}
		})
		return nil
	})
}
