package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	sq "github.com/Masterminds/squirrel"

	// import the postgresql driver
	_ "github.com/lib/pq"
)

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func failOnError(err error, msg string) {
	if err != nil {
		fmt.Println(msg, err)
		os.Exit(1)
	}
}

func main() {
	db, err := sql.Open("postgres", os.Getenv("DB_URL"))
	failOnError(err, "failed to open database")
	defer func() {
		if err := db.Close(); err != nil {
			failOnError(err, "failed to close database")
		}
	}()

	if err := db.Ping(); err != nil {
		failOnError(err, "failed to ping database")
	}

	repo := NewRepository(db)
	svc := NewService(repo)
	h := NewHandler(svc)

	e := echo.New()
	e.Use(middleware.CORS())
	e.Use(middleware.Logger())
	e.HTTPErrorHandler = helper

	route := e.Group("/api/v1")
	route.GET("/provinces", h.GetAll)
	route.GET("/provinces/:id", h.GetByID)

	go func() {
		if err := e.Start(fmt.Sprintf(":%s", getEnv("PORT", "8080"))); err != nil {
			e.Logger.Fatal("Shutting down the server")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("Shutdown in progress...")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		e.Logger.Fatal("Failed to shutdown the server", err)
	}
}

func helper(err error, c echo.Context) {
	switch err {
	case ErrInvalidParamInt:
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "invalid parameter",
			"message": err.Error(),
		})

	case ErrUnknownProvince:
		c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "requested item not found",
			"message": err.Error(),
		})

	default:
		c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "internal server error",
			"message": "something went wrong",
		})
	}
}

// ErrInvalidParamInt is an error when int param not valid.
var ErrInvalidParamInt = errors.New("param: '<attribute>' cannot be applied because the value is not a number")

// intParam is a validator for integer parameters.
func intParam(v string) (int, error) {
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, ErrInvalidParamInt
	}
	return i, nil
}

type handler struct {
	service *Service
}

// NewHandler creates a new handler
func NewHandler(s *Service) *handler {
	return &handler{s}
}

func (h *handler) GetAll(c echo.Context) error {
	provinces, err := h.service.GetProvinces(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, provinces)
}

func (h *handler) GetByID(c echo.Context) error {
	id, err := intParam(c.Param("id"))
	if err != nil {
		return err
	}
	p, err := h.service.GetProvinceByID(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

type Service struct {
	repo *Repository
}

// NewService creates a new service
func NewService(r *Repository) *Service {
	return &Service{r}
}

func (s *Service) GetProvinces(ctx context.Context) ([]Province, error) {
	return s.repo.GetProvinces(ctx)
}

func (s *Service) GetProvinceByID(ctx context.Context, provinceID int) (*Province, error) {
	p, err := s.repo.GetProvinceByID(ctx, provinceID)
	if err != nil {
		return nil, err
	}
	cities, err := s.repo.GetCities(ctx, provinceID)
	if err != nil {
		return nil, err
	}
	return assemble(&p, cities), nil
}

func assemble(province *Province, cities []City) *Province {
	province.Cities = cities
	return province
}

// ErrUnknownProvince is returned when a province could not be found.
var ErrUnknownProvince = errors.New("unknown province")

// Province represents a province.
type Province struct {
	ID          int    `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	NameEnglish string `json:"name_english"`

	// Cities represents a list of cities in the province.
	Cities []City `json:"cities,omitempty"`
}

// City represents a city.
type City struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	NameEnglish string `json:"name_english"`
}

type Repository struct {
	db *sql.DB
}

// NewRepository creates a new repository
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetProvinces(ctx context.Context) ([]Province, error) {
	q, args, err := sq.Select("id", "name", "name_english", "code").
		From("tb_provinces").
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return nil, err
	}
	provinces := make([]Province, 0)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		p, err := scanProvince(rows.Scan)
		if err != nil {
			return nil, err
		}
		provinces = append(provinces, p)
	}
	return provinces, nil
}

func (r *Repository) GetProvinceByID(ctx context.Context, provinceID int) (Province, error) {
	q, args, err := sq.Select("id", "name", "name_english", "code").
		From("tb_provinces").
		Where("id = ?", provinceID).
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return Province{}, err
	}
	row := r.db.QueryRowContext(ctx, q, args...)
	p, err := scanProvince(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Province{}, ErrUnknownProvince
	}
	if err != nil {
		return Province{}, err
	}
	return p, err
}

func (r *Repository) GetCities(ctx context.Context, provinceID int) ([]City, error) {
	q, args, err := sq.Select("id", "name", "name_english").
		From("tb_cities").
		Where(sq.Eq{"province_id": provinceID}).
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return nil, err
	}
	cities := make([]City, 0)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		c, err := scanCity(rows.Scan)
		if err != nil {
			return nil, err
		}
		cities = append(cities, c)
	}
	return cities, nil
}

func scanProvince(scan func(...any) error) (p Province, _ error) {
	return p, scan(&p.ID, &p.Name, &p.NameEnglish, &p.Code)
}

func scanCity(scan func(...any) error) (c City, _ error) {
	return c, scan(&c.ID, &c.Name, &c.NameEnglish)
}
