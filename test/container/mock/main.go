// Command mock starts one synthetic HTTPS Redfish service for container tests.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const listenAddress = ":8443"

type configuration struct {
	id                 string
	username           string
	password           string
	powerWatts         float64
	temperatureCelsius float64
	humidityPercent    float64
	fanRPM             float64
}

func main() {
	if err := run(); err != nil {
		slog.Error("synthetic Redfish service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := loadConfiguration()
	if err != nil {
		return err
	}
	certificate, err := newCertificate(config.id)
	if err != nil {
		return fmt.Errorf("create synthetic TLS certificate: %w", err)
	}
	server := &http.Server{
		Addr:              listenAddress,
		Handler:           http.HandlerFunc(config.serveHTTP),
		ReadHeaderTimeout: 2 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		},
	}
	listener, err := tls.Listen("tcp", listenAddress, server.TLSConfig)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	slog.Info("synthetic Redfish service listening", "id", config.id, "address", listenAddress)
	select {
	case serveErr := <-serveDone:
		return normalizeServerError(serveErr)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	serveErr := <-serveDone
	return errors.Join(shutdownErr, normalizeServerError(serveErr))
}

func loadConfiguration() (configuration, error) {
	result := configuration{
		id:       strings.TrimSpace(os.Getenv("REDFISH_MOCK_ID")),
		username: os.Getenv("REDFISH_MOCK_USERNAME"),
		password: os.Getenv("REDFISH_MOCK_PASSWORD"),
	}
	if result.id == "" || result.username == "" || result.password == "" {
		return configuration{}, errors.New("REDFISH_MOCK_ID, REDFISH_MOCK_USERNAME and REDFISH_MOCK_PASSWORD are required")
	}
	readings := []struct {
		name        string
		destination *float64
	}{
		{name: "REDFISH_MOCK_POWER_WATTS", destination: &result.powerWatts},
		{name: "REDFISH_MOCK_TEMPERATURE_CELSIUS", destination: &result.temperatureCelsius},
		{name: "REDFISH_MOCK_HUMIDITY_PERCENT", destination: &result.humidityPercent},
		{name: "REDFISH_MOCK_FAN_RPM", destination: &result.fanRPM},
	}
	for _, reading := range readings {
		value, err := requiredFloat(reading.name)
		if err != nil {
			return configuration{}, err
		}
		*reading.destination = value
	}
	return result, nil
}

func requiredFloat(name string) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func (c configuration) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/" {
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":      "/redfish/v1/",
			"@odata.type":    "#ServiceRoot.v1_20_0.ServiceRoot",
			"Id":             "RootService",
			"Name":           "Synthetic Redfish Service " + c.id,
			"RedfishVersion": "1.20.0",
			"Systems":        map[string]string{"@odata.id": "/redfish/v1/Systems"},
			"Chassis":        map[string]string{"@odata.id": "/redfish/v1/Chassis"},
		})
		return
	}
	if !c.authorized(request) {
		writeRedfishError(writer, http.StatusUnauthorized)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Systems":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":           "/redfish/v1/Systems",
			"@odata.type":         "#ComputerSystemCollection.ComputerSystemCollection",
			"Name":                "Computer System Collection",
			"Members@odata.count": 1,
			"Members": []map[string]string{
				{"@odata.id": "/redfish/v1/Systems/1"},
			},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Systems/1":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":    "/redfish/v1/Systems/1",
			"@odata.type":  "#ComputerSystem.v1_29_0.ComputerSystem",
			"Id":           "system-1",
			"Name":         "Synthetic Server " + c.id,
			"Manufacturer": "Kubling",
			"Model":        "Virtual Redfish Node",
			"SerialNumber": "SYNTHETIC-" + strings.ToUpper(c.id),
			"HostName":     c.id + ".example.test",
			"PowerState":   "On",
			"Status": map[string]string{
				"State":  "Enabled",
				"Health": "OK",
			},
			"Links": map[string]any{
				"Chassis": []map[string]string{{"@odata.id": "/redfish/v1/Chassis/1"}},
			},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":           "/redfish/v1/Chassis",
			"@odata.type":         "#ChassisCollection.ChassisCollection",
			"Name":                "Chassis Collection",
			"Members@odata.count": 1,
			"Members": []map[string]string{
				{"@odata.id": "/redfish/v1/Chassis/1"},
			},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":    "/redfish/v1/Chassis/1",
			"@odata.type":  "#Chassis.v1_29_0.Chassis",
			"Id":           "chassis-1",
			"Name":         "Synthetic Chassis " + c.id,
			"ChassisType":  "RackMount",
			"Manufacturer": "Kubling",
			"Model":        "Virtual Redfish Chassis",
			"SerialNumber": "CHASSIS-" + strings.ToUpper(c.id),
			"PowerState":   "On",
			"Status": map[string]string{
				"State":  "Enabled",
				"Health": "OK",
			},
			"PowerSubsystem":     map[string]string{"@odata.id": "/redfish/v1/Chassis/1/PowerSubsystem"},
			"EnvironmentMetrics": map[string]string{"@odata.id": "/redfish/v1/Chassis/1/EnvironmentMetrics"},
			"ThermalSubsystem":   map[string]string{"@odata.id": "/redfish/v1/Chassis/1/ThermalSubsystem"},
			"Sensors":            map[string]string{"@odata.id": "/redfish/v1/Chassis/1/Sensors"},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1/PowerSubsystem":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":     "/redfish/v1/Chassis/1/PowerSubsystem",
			"@odata.type":   "#PowerSubsystem.v1_2_0.PowerSubsystem",
			"Id":            "PowerSubsystem",
			"Name":          "Synthetic Power Subsystem " + c.id,
			"CapacityWatts": 1600,
			"Allocation": map[string]float64{
				"AllocatedWatts": c.powerWatts,
				"RequestedWatts": c.powerWatts,
			},
			"Status": map[string]string{
				"State":  "Enabled",
				"Health": "OK",
			},
			"PowerSupplies": map[string]string{"@odata.id": "/redfish/v1/Chassis/1/PowerSubsystem/PowerSupplies"},
			"PowerSupplyRedundancy": []map[string]any{{
				"GroupName":        "Synthetic PSU pair",
				"RedundancyType":   "NPlusM",
				"MinNeededInGroup": 1,
				"Status":           map[string]string{"State": "Enabled", "Health": "OK"},
				"RedundancyGroup":  []any{},
			}},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1/PowerSubsystem/PowerSupplies":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":           "/redfish/v1/Chassis/1/PowerSubsystem/PowerSupplies",
			"@odata.type":         "#PowerSupplyCollection.PowerSupplyCollection",
			"Name":                "Power Supply Collection",
			"Members@odata.count": 1,
			"Members": []map[string]any{{
				"@odata.id":          "/redfish/v1/Chassis/1/PowerSubsystem/PowerSupplies/1",
				"@odata.type":        "#PowerSupply.v1_8_0.PowerSupply",
				"Id":                 "psu-1",
				"Name":               "Synthetic PSU " + c.id,
				"Manufacturer":       "Kubling",
				"Model":              "Virtual PSU",
				"SerialNumber":       "PSU-" + strings.ToUpper(c.id),
				"PowerCapacityWatts": 1600,
				"PowerSupplyType":    "AC",
				"Status":             map[string]string{"State": "Enabled", "Health": "OK"},
			}},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1/EnvironmentMetrics":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":   "/redfish/v1/Chassis/1/EnvironmentMetrics",
			"@odata.type": "#EnvironmentMetrics.v1_7_0.EnvironmentMetrics",
			"Id":          "EnvironmentMetrics",
			"Name":        "Synthetic Environment Metrics " + c.id,
			"PowerWatts": map[string]float64{
				"Reading": c.powerWatts,
			},
			"EnergykWh": map[string]float64{
				"Reading": c.powerWatts / 10,
			},
			"TemperatureCelsius": map[string]float64{
				"Reading": c.temperatureCelsius,
			},
			"AmbientTemperatureCelsius": map[string]float64{
				"Reading": c.temperatureCelsius - 5,
			},
			"HumidityPercent": map[string]float64{
				"Reading": c.humidityPercent,
			},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1/ThermalSubsystem":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":   "/redfish/v1/Chassis/1/ThermalSubsystem",
			"@odata.type": "#ThermalSubsystem.v1_5_0.ThermalSubsystem",
			"Id":          "ThermalSubsystem",
			"Name":        "Synthetic Thermal Subsystem " + c.id,
			"Fans":        map[string]string{"@odata.id": "/redfish/v1/Chassis/1/ThermalSubsystem/Fans"},
			"Status":      map[string]string{"State": "Enabled", "Health": "OK"},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1/ThermalSubsystem/Fans":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":           "/redfish/v1/Chassis/1/ThermalSubsystem/Fans",
			"@odata.type":         "#FanCollection.FanCollection",
			"Name":                "Fan Collection",
			"Members@odata.count": 1,
			"Members": []map[string]any{{
				"@odata.id":   "/redfish/v1/Chassis/1/ThermalSubsystem/Fans/1",
				"@odata.type": "#Fan.v1_6_0.Fan",
				"Id":          "fan-1",
				"Name":        "Synthetic Fan " + c.id,
				"SpeedPercent": map[string]float64{
					"Reading":  55,
					"SpeedRPM": c.fanRPM,
				},
				"Status": map[string]string{"State": "Enabled", "Health": "OK"},
			}},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/redfish/v1/Chassis/1/Sensors":
		writeJSON(writer, http.StatusOK, map[string]any{
			"@odata.id":           "/redfish/v1/Chassis/1/Sensors",
			"@odata.type":         "#SensorCollection.SensorCollection",
			"Name":                "Sensor Collection",
			"Members@odata.count": 2,
			"Members": []map[string]any{
				{
					"@odata.id":       "/redfish/v1/Chassis/1/Sensors/Temperature1",
					"@odata.type":     "#Sensor.v1_14_0.Sensor",
					"Id":              "temperature-1",
					"Name":            "CPU Temperature " + c.id,
					"ReadingType":     "Temperature",
					"Reading":         c.temperatureCelsius,
					"PhysicalContext": "CPU",
					"Thresholds": map[string]any{
						"UpperCaution":  map[string]float64{"Reading": 75},
						"UpperCritical": map[string]float64{"Reading": 90},
					},
					"Status": map[string]string{"State": "Enabled", "Health": "OK"},
				},
				{
					"@odata.id":   "/redfish/v1/Chassis/1/Sensors/Voltage1",
					"@odata.type": "#Sensor.v1_14_0.Sensor",
					"Id":          "voltage-1",
					"Name":        "Synthetic Voltage " + c.id,
					"ReadingType": "Voltage",
					"Reading":     12,
				},
			},
		})
	default:
		writeRedfishError(writer, http.StatusNotFound)
	}
}

func (c configuration) authorized(request *http.Request) bool {
	username, password, ok := request.BasicAuth()
	if !ok {
		return false
	}
	usernameMatches := subtle.ConstantTimeCompare([]byte(username), []byte(c.username)) == 1
	passwordMatches := subtle.ConstantTimeCompare([]byte(password), []byte(c.password)) == 1
	return usernameMatches && passwordMatches
}

func writeJSON(writer http.ResponseWriter, statusCode int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeRedfishError(writer http.ResponseWriter, statusCode int) {
	writeJSON(writer, statusCode, map[string]any{
		"error": map[string]string{
			"code":    http.StatusText(statusCode),
			"message": "synthetic Redfish request failed",
		},
	})
}

func newCertificate(id string) (tls.Certificate, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Kubling synthetic Redfish " + id},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{id, "localhost"},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	return tls.X509KeyPair(certificatePEM, privateKeyPEM)
}

func normalizeServerError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
