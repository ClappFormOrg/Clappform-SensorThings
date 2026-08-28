// Command sulo-probe is a read-only diagnostic for the SULO (REEN) adapter.
//
// It exercises exactly the calls the scheduler makes — open a session, list
// container slots, list each slot's Datastream, fetch recent observations —
// and prints what came back. It never writes: no FROST target, no state
// store, no Postgres. Use it to prove credentials and data flow before
// starting the translation layer, and after any credential rotation.
//
//	go run ./cmd/sulo-probe                 # reads .env, samples 3 slots
//	go run ./cmd/sulo-probe -slots 10 -lookback 720h
//	go run ./cmd/sulo-probe -all            # list every slot found
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters/sulo"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

func main() {
	envPath := flag.String("env", ".env", "path to an env file to load (values already in the environment win); empty to skip")
	lookback := flag.Duration("lookback", 7*24*time.Hour, "how far back to ask REEN for observations")
	sample := flag.Int("slots", 3, "how many container slots to sample observations for")
	all := flag.Bool("all", false, "list every discovered container slot, not just a preview")
	debug := flag.Bool("debug", false, "verbose adapter logging")
	flag.Parse()

	if err := run(*envPath, *lookback, *sample, *all, *debug); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAILED: %v\n", err)
		os.Exit(1)
	}
}

func run(envPath string, lookback time.Duration, sample int, all, debug bool) error {
	if envPath != "" {
		loaded, err := loadEnvFile(envPath)
		if err != nil {
			fmt.Printf("note: could not read %s (%v) — relying on the ambient environment\n\n", envPath, err)
		} else {
			fmt.Printf("loaded %d variables from %s\n\n", loaded, envPath)
		}
	}

	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := sulo.Config{
		BaseURL:                os.Getenv("SULO_API_BASE_URL"),
		Username:               os.Getenv("SULO_API_USERNAME"),
		Password:               os.Getenv("SULO_API_PASSWORD"),
		CustomerID:             os.Getenv("SULO_CUSTOMER_ID"),
		PageSize:               intEnv("SULO_PAGE_SIZE"),
		ObservationPageLimit:   intEnv("SULO_OBSERVATION_PAGE_LIMIT"),
		MinConfidence:          intEnv("SULO_MIN_CONFIDENCE"),
		ExpectedCadenceSeconds: intEnv("SULO_EXPECTED_CADENCE_SECONDS"),
	}
	if s := intEnv("SULO_HTTP_TIMEOUT_SECONDS"); s > 0 {
		cfg.HTTPTimeout = time.Duration(s) * time.Second
	}

	fmt.Println("-- configuration ---------------------------------------------")
	fmt.Printf("  base URL    : %s\n", orUnset(cfg.BaseURL))
	fmt.Printf("  username    : %s\n", orUnset(cfg.Username))
	fmt.Printf("  password    : %s\n", masked(cfg.Password))
	fmt.Printf("  customer id : %s\n", orDefault(cfg.CustomerID, "(account default)"))
	fmt.Printf("  min conf.   : %s\n", orDefault(itoaIfSet(cfg.MinConfidence), "1 (adapter default - drops only erroneous readings)"))
	fmt.Println()

	adapter, err := sulo.New(cfg, logger)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Step 1 - discovery. This is also the session/auth check: ListThings
	// cannot succeed without a working POST /session.
	fmt.Println("-- step 1: session + discovery (GET /containerSlots, /sites, /devices/linked, /contentTypes)")
	start := time.Now()
	things, err := adapter.ListThings(ctx)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	fmt.Printf("  OK  %d container slots published in %s\n\n", len(things), time.Since(start).Round(time.Millisecond))

	if len(things) == 0 {
		fmt.Println("  No slots came back. Either this REEN customer has none, or every slot")
		fmt.Println("  was skipped for missing site coordinates - re-run with -debug to see the")
		fmt.Println("  skip warning and the affected slot ids.")
		return nil
	}

	preview := things
	if !all && len(preview) > 5 {
		preview = preview[:5]
	}
	fmt.Println("-- discovered slots ------------------------------------------")
	for _, t := range preview {
		fmt.Printf("  slot %-8s  lon=%9.5f lat=%9.5f  %s\n",
			t.VendorNativeID, t.Location.Lon, t.Location.Lat, t.Properties["reen_site_name"])
		fmt.Printf("      %s\n", t.Description)
	}
	if len(things) > len(preview) {
		fmt.Printf("  ... and %d more (-all to list them)\n", len(things)-len(preview))
	}
	fmt.Println()

	// Step 2 - per-slot streams and observations, exactly as the scheduler
	// would drive them.
	if sample > len(things) {
		sample = len(things)
	}
	since := time.Now().UTC().Add(-lookback)
	fmt.Printf("-- step 2: observations since %s (lookback %s), sampling %d slot(s)\n",
		since.Format(time.RFC3339), lookback, sample)

	totalObs := 0
	withData := 0
	for _, t := range things[:sample] {
		streams, err := adapter.ListDatastreamsForThing(ctx, t.VendorNativeID)
		if err != nil {
			return fmt.Errorf("slot %s: list datastreams: %w", t.VendorNativeID, err)
		}
		for _, d := range streams {
			obs, err := adapter.FetchObservations(ctx, t.VendorNativeID, d.ObservedProperty, since, 0)
			if err != nil {
				return fmt.Errorf("slot %s: fetch observations: %w", t.VendorNativeID, err)
			}
			totalObs += len(obs)
			if len(obs) > 0 {
				withData++
			}
			fmt.Printf("\n  slot %s - %s (%s), sensor model %q\n",
				t.VendorNativeID, d.ObservedProperty, d.Unit, d.SensorMetadata["model"])
			fmt.Printf("    %d observations\n", len(obs))
			describeObs(obs)
		}
	}

	fmt.Println("\n-- summary ---------------------------------------------------")
	fmt.Printf("  slots discovered      : %d\n", len(things))
	fmt.Printf("  slots sampled         : %d\n", sample)
	fmt.Printf("  slots returning data  : %d\n", withData)
	fmt.Printf("  observations fetched  : %d\n", totalObs)
	fmt.Println()

	if totalObs == 0 {
		fmt.Println("  Session and discovery work, but no observations landed in the window.")
		fmt.Println("  Try a longer -lookback, or -slots to sample more of the estate. If it")
		fmt.Println("  stays empty across a wide window, check the account's data rights in REEN.")
		return nil
	}
	fmt.Println("  SULO adapter is working end to end (read side).")
	fmt.Println("  Set SULO_EXPECTED_CADENCE_SECONDS from the observed spacing above so the")
	fmt.Println("  freshness watchdog uses a per-stream threshold instead of the global one.")
	return nil
}

// describeObs prints the shape of a fetched series: the first and last few
// readings plus the median gap, which is what you need to pick a cadence.
func describeObs(obs []canonical.Observation) {
	if len(obs) == 0 {
		return
	}
	show := func(o canonical.Observation) {
		fmt.Printf("      %s  %6.2f %%\n", o.PhenomenonTime.Format(time.RFC3339), o.Result)
	}
	const edge = 3
	if len(obs) <= 2*edge {
		for _, o := range obs {
			show(o)
		}
	} else {
		for _, o := range obs[:edge] {
			show(o)
		}
		fmt.Printf("      ... %d more ...\n", len(obs)-2*edge)
		for _, o := range obs[len(obs)-edge:] {
			show(o)
		}
	}

	if len(obs) < 2 {
		return
	}
	gaps := make([]time.Duration, 0, len(obs)-1)
	for i := 1; i < len(obs); i++ {
		gaps = append(gaps, obs[i].PhenomenonTime.Sub(obs[i-1].PhenomenonTime))
	}
	// Median gap: robust against the odd burst or outage.
	for i := 1; i < len(gaps); i++ {
		for j := i; j > 0 && gaps[j] < gaps[j-1]; j-- {
			gaps[j], gaps[j-1] = gaps[j-1], gaps[j]
		}
	}
	med := gaps[len(gaps)/2]
	fmt.Printf("    span %s -> %s, median gap %s (approx SULO_EXPECTED_CADENCE_SECONDS=%d)\n",
		obs[0].PhenomenonTime.Format(time.RFC3339),
		obs[len(obs)-1].PhenomenonTime.Format(time.RFC3339),
		med.Round(time.Second), int(med.Seconds()))
}

// loadEnvFile applies KEY=VALUE lines from path. Variables already present
// in the environment are left alone, so an explicit export still wins.
func loadEnvFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return n, err
		}
		n++
	}
	return n, sc.Err()
}

func intEnv(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return 0
	}
	return n
}

func itoaIfSet(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func orUnset(s string) string {
	if s == "" {
		return "(UNSET)"
	}
	return s
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func masked(s string) string {
	if s == "" {
		return "(UNSET)"
	}
	return fmt.Sprintf("(set, %d chars)", len(s))
}
