package shop

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mroth/weightedrand"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/cloudhut/owl-shop/pkg/config"
	"github.com/cloudhut/owl-shop/pkg/kafka"
	"github.com/cloudhut/owl-shop/pkg/sr"
)

type Shop struct {
	cfg    config.Config
	logger *zap.Logger

	chooser *weightedrand.Chooser

	// Services
	customerSvc *CustomerService
}

func New(cfg config.Config, logger *zap.Logger) (*Shop, error) {
	kafkaFactory := kafka.NewFactory(cfg.Kafka, logger.Named("kafka_client"))
	schemaFactory := sr.NewFactory(cfg.SchemaRegistry, logger.Named("schema_registry"))

	// srClient may be nil if schema registry hasn't been configured
	srClient, err := schemaFactory.NewSchemaRegistryClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create schema registry client")
	}

	customerSvc, err := NewCustomerService(cfg.Shop, logger, kafkaFactory)
	if err != nil {
		return nil, fmt.Errorf("failed to create customer service: %w", err)
	}

	addressSvc, err := NewAddressService(cfg.Shop, logger.Named("address_svc"), kafkaFactory)
	if err != nil {
		return nil, fmt.Errorf("failed to create address service: %w", err)
	}

	frontendSvc, err := NewFrontendService(cfg.Shop, logger.Named("frontend_svc"), kafkaFactory)
	if err != nil {
		return nil, fmt.Errorf("failed to create frontend service: %w", err)
	}

	orderSvc, err := NewOrderService(cfg.Shop, logger.Named("order_svc"), kafkaFactory, srClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create order service: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	err = customerSvc.Initialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize customer service: %w", err)
	}

	err = addressSvc.Initialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize address service: %w", err)
	}

	err = frontendSvc.Initialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize frontend service: %w", err)
	}

	err = orderSvc.Initialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize order service: %w", err)
	}

	go addressSvc.Start()
	go orderSvc.Start()

	// Random chooser
	wr, err := weightedrand.NewChooser(
		weightedrand.Choice{Item: frontendSvc.CreateFrontendEvent, Weight: 1000},
		weightedrand.Choice{Item: customerSvc.CreateCustomer, Weight: 50},
		weightedrand.Choice{Item: addressSvc.CreateAddress, Weight: 30},
		weightedrand.Choice{Item: customerSvc.DeleteCustomer, Weight: 8},
		weightedrand.Choice{Item: customerSvc.ModifyCustomer, Weight: 6},
		weightedrand.Choice{Item: orderSvc.CreateOrder, Weight: 5},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create random chooser: %w", err)
	}

	return &Shop{
		cfg:    cfg,
		logger: logger,

		chooser: wr,

		customerSvc: customerSvc,
	}, nil
}

// Start starts all shop components and triggers events (e.g. customer registration) in accordance with the
// config for traffic simulation.
func (s *Shop) Start() error {
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe(":8080", nil)
		s.logger.Info("prometheus http handler quit", zap.Error(err))
	}()

	for {
		for i := 0; i < s.cfg.Shop.RequestRate; i++ {
			pageImpressionsSimulated.Inc()
			s.SimulatePageImpression()
		}
		time.Sleep(s.cfg.Shop.RequestRateInterval)
	}
}

// SimulatePageImpression simulates a user visiting a page in our imaginary owl shop. This page impression can be a
// user registration, oder, viewing articles or doing anything else a common user would do in a shop.
func (s *Shop) SimulatePageImpression() {

	go func() {
		fn, isOk := s.chooser.Pick().(func())
		if !isOk {
			s.logger.Fatal("randomly picked method is not a func")
		}
		fn()
	}()
}
