package server

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
)

func setupTestServer(t *testing.T) *Server {
	t.Helper()
	tempDir := t.TempDir()
	usersPath := filepath.Join(tempDir, "users.json")

	srv, err := NewServer(":0", usersPath, "empresa-a")
	if err != nil {
		t.Fatalf("falha ao criar servidor de teste: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func registerDriverAndVehicle(t *testing.T, srv *Server, email string) *Session {
	t.Helper()
	session := NewSession()
	regResp := srv.handleRegisterUser(mustJSON(protocol.CreateUserRequest{
		Name:     "Motorista Teste",
		Email:    email,
		Password: "Password123!",
		Role:     models.RoleDriver,
	}))
	if regResp.Success != "success" {
		t.Fatalf("falha ao registrar motorista: %s", regResp.Message)
	}

	loginResp := srv.handleLogin(mustJSON(protocol.LoginRequest{
		Email:    email,
		Password: "Password123!",
	}), session)
	if loginResp.Success != "success" {
		t.Fatalf("falha ao logar motorista: %s", loginResp.Message)
	}

	vehResp := srv.handleRegisterVehicle(mustJSON(protocol.CreateVehicleRequest{
		Plate:        "ABC-1D23",
		Model:        "Sedan Test",
		Color:        "Preto",
		SeatCapacity: 4,
	}), session)
	if vehResp.Success != "success" {
		t.Fatalf("falha ao registrar veiculo: %s", vehResp.Message)
	}

	return session
}

func registerPassenger(t *testing.T, srv *Server, email string) *Session {
	t.Helper()
	session := NewSession()
	regResp := srv.handleRegisterUser(mustJSON(protocol.CreateUserRequest{
		Name:     "Passageiro Teste",
		Email:    email,
		Password: "Password123!",
		Role:     models.RolePassenger,
	}))
	if regResp.Success != "success" {
		t.Fatalf("falha ao registrar passageiro: %s", regResp.Message)
	}

	loginResp := srv.handleLogin(mustJSON(protocol.LoginRequest{
		Email:    email,
		Password: "Password123!",
	}), session)
	if loginResp.Success != "success" {
		t.Fatalf("falha ao logar passageiro: %s", loginResp.Message)
	}

	return session
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestRideLifecycle(t *testing.T) {
	srv := setupTestServer(t)
	driverSession := registerDriverAndVehicle(t, srv, "driver1@vaijunto.test")

	// 1. Criar carona
	depAt := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	arrAt := depAt.Add(1 * time.Hour)
	createResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depAt,
		Segments: []protocol.CreateStageRequest{
			{
				Origin:         enum.Salvador,
				Destination:    enum.FeiraDeSantana,
				DepartureAt:    depAt,
				ArrivalAt:      arrAt,
				PriceCents:     3500,
				AvailableSeats: 3,
			},
		},
	}), driverSession)
	if createResp.Success != "success" {
		t.Fatalf("erro ao criar carona: %s", createResp.Message)
	}

	var ride models.Ride
	_ = json.Unmarshal(createResp.Payload, &ride)
	if ride.GetStatus() != models.RideStatusScheduled {
		t.Fatalf("status inicial esperado SCHEDULED, obtido: %s", ride.GetStatus())
	}

	// 2. Tentar finalizar carona que ainda nem iniciou -> DEVE FALHAR
	finishEarly := srv.handleFinishRide(mustJSON(protocol.FinishRideRequest{
		RideID: ride.ID,
	}), driverSession)
	if finishEarly.Success != "error" {
		t.Fatalf("esperava erro ao finalizar carona nao iniciada, obteve sucesso")
	}

	// 3. Iniciar carona -> SUCESSO
	startResp := srv.handleStartRide(mustJSON(protocol.StartRideRequest{
		RideID: ride.ID,
	}), driverSession)
	if startResp.Success != "success" {
		t.Fatalf("erro ao iniciar carona: %s", startResp.Message)
	}

	var startedRide models.Ride
	_ = json.Unmarshal(startResp.Payload, &startedRide)
	if startedRide.GetStatus() != models.RideStatusInProgress {
		t.Fatalf("status esperado IN_PROGRESS, obtido: %s", startedRide.GetStatus())
	}
	if startedRide.StartedAt == nil {
		t.Fatalf("started_at nao deveria ser nulo apos iniciar")
	}

	// 4. Tentar iniciar novamente -> DEVE FALHAR
	startAgain := srv.handleStartRide(mustJSON(protocol.StartRideRequest{
		RideID: ride.ID,
	}), driverSession)
	if startAgain.Success != "error" {
		t.Fatalf("esperava erro ao tentar iniciar carona ja em andamento")
	}

	// 5. Finalizar carona -> SUCESSO
	finishResp := srv.handleFinishRide(mustJSON(protocol.FinishRideRequest{
		RideID: ride.ID,
	}), driverSession)
	if finishResp.Success != "success" {
		t.Fatalf("erro ao finalizar carona: %s", finishResp.Message)
	}

	var finishedRide models.Ride
	_ = json.Unmarshal(finishResp.Payload, &finishedRide)
	if finishedRide.GetStatus() != models.RideStatusCompleted {
		t.Fatalf("status esperado COMPLETED, obtido: %s", finishedRide.GetStatus())
	}
	if finishedRide.CompletedAt == nil {
		t.Fatalf("completed_at nao deveria ser nulo apos finalizar")
	}
}

func TestRideRatingAndReputation(t *testing.T) {
	srv := setupTestServer(t)
	driverSession := registerDriverAndVehicle(t, srv, "driver2@vaijunto.test")
	passengerSession := registerPassenger(t, srv, "pax1@vaijunto.test")

	depAt := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	arrAt := depAt.Add(1 * time.Hour)
	createResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depAt,
		Segments: []protocol.CreateStageRequest{
			{
				Origin:         enum.Salvador,
				Destination:    enum.FeiraDeSantana,
				DepartureAt:    depAt,
				ArrivalAt:      arrAt,
				PriceCents:     3500,
				AvailableSeats: 3,
			},
		},
	}), driverSession)

	var ride models.Ride
	_ = json.Unmarshal(createResp.Payload, &ride)
	segmentID := ride.Segments[0].ID

	// Passageiro reserva a carona
	bookResp := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{
				RideID:    ride.ID,
				SegmentID: segmentID,
				CompanyID: "empresa-a",
			},
		},
	}), passengerSession)
	if bookResp.Success != "success" {
		t.Fatalf("erro ao reservar carona: %s", bookResp.Message)
	}

	var res protocol.ReservationResponse
	_ = json.Unmarshal(bookResp.Payload, &res)

	// 1. Passageiro tenta avaliar motorista antes da viagem ser finalizada -> DEVE FALHAR
	rateEarly := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res.ID,
		DriverID:      driverSession.UserID,
		Score:         5,
		Comment:       "Tentativa prematura",
	}), passengerSession)
	if rateEarly.Success != "error" {
		t.Fatalf("esperava erro ao tentar avaliar carona nao concluida")
	}

	// 2. Inicia e finaliza a carona
	_ = srv.handleStartRide(mustJSON(protocol.StartRideRequest{RideID: ride.ID}), driverSession)
	_ = srv.handleFinishRide(mustJSON(protocol.FinishRideRequest{RideID: ride.ID}), driverSession)

	// 3. Passageiro avalia motorista com nota 5 -> SUCESSO
	rateDriverResp := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res.ID,
		DriverID:      driverSession.UserID,
		Score:         5,
		Comment:       "Viagem excelente e pontual!",
	}), passengerSession)
	if rateDriverResp.Success != "success" {
		t.Fatalf("erro ao avaliar motorista: %s", rateDriverResp.Message)
	}

	// 4. Passageiro tenta avaliar o mesmo motorista novamente na mesma carona -> DEVE FALHAR (Anti-Fraude/Duplicidade)
	rateDriverDup := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res.ID,
		DriverID:      driverSession.UserID,
		Score:         4,
		Comment:       "Duplicada",
	}), passengerSession)
	if rateDriverDup.Success != "error" {
		t.Fatalf("esperava erro ao tentar enviar avaliacao duplicada")
	}

	// 5. Motorista avalia o passageiro com nota 4 -> SUCESSO
	ratePaxResp := srv.handleRatePassenger(mustJSON(protocol.RatePassengerRequest{
		RideID:        ride.ID,
		ReservationID: res.ID,
		PassengerID:   passengerSession.UserID,
		Score:         4,
		Comment:       "Bom passageiro.",
	}), driverSession)
	if ratePaxResp.Success != "success" {
		t.Fatalf("erro ao avaliar passageiro: %s", ratePaxResp.Message)
	}

	// 6. Motorista tenta avaliar o mesmo passageiro novamente na mesma carona -> DEVE FALHAR
	ratePaxDup := srv.handleRatePassenger(mustJSON(protocol.RatePassengerRequest{
		RideID:        ride.ID,
		ReservationID: res.ID,
		PassengerID:   passengerSession.UserID,
		Score:         3,
		Comment:       "Duplicada",
	}), driverSession)
	if ratePaxDup.Success != "error" {
		t.Fatalf("esperava erro ao tentar enviar avaliacao duplicada para passageiro")
	}

	// 7. Consultar reputação do motorista
	driverRepResp := srv.handleGetUserReputation(mustJSON(protocol.GetUserReputationRequest{
		UserID: driverSession.UserID,
	}), passengerSession)
	if driverRepResp.Success != "success" {
		t.Fatalf("erro ao consultar reputacao do motorista: %s", driverRepResp.Message)
	}

	var driverRep models.UserReputation
	_ = json.Unmarshal(driverRepResp.Payload, &driverRep)
	if driverRep.TotalRatings != 1 || driverRep.AverageScore != 5.0 {
		t.Fatalf("esperado 1 avaliacao media 5.0, obtido: count=%d avg=%.2f", driverRep.TotalRatings, driverRep.AverageScore)
	}

	// 8. Consultar reputação do passageiro
	paxRepResp := srv.handleGetUserReputation(mustJSON(protocol.GetUserReputationRequest{
		UserID: passengerSession.UserID,
	}), driverSession)
	if paxRepResp.Success != "success" {
		t.Fatalf("erro ao consultar reputacao do passageiro: %s", paxRepResp.Message)
	}

	var paxRep models.UserReputation
	_ = json.Unmarshal(paxRepResp.Payload, &paxRep)
	if paxRep.TotalRatings != 1 || paxRep.AverageScore != 4.0 {
		t.Fatalf("esperado 1 avaliacao media 4.0, obtido: count=%d avg=%.2f", paxRep.TotalRatings, paxRep.AverageScore)
	}
}

func TestNoShowReporting(t *testing.T) {
	srv := setupTestServer(t)
	driverSession := registerDriverAndVehicle(t, srv, "driver3@vaijunto.test")
	passengerSession := registerPassenger(t, srv, "pax2@vaijunto.test")

	depAt := time.Now().Add(1 * time.Hour).Truncate(time.Minute)
	arrAt := depAt.Add(1 * time.Hour)
	createResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depAt,
		Segments: []protocol.CreateStageRequest{
			{
				Origin:         enum.Salvador,
				Destination:    enum.FeiraDeSantana,
				DepartureAt:    depAt,
				ArrivalAt:      arrAt,
				PriceCents:     3500,
				AvailableSeats: 3,
			},
		},
	}), driverSession)

	var ride models.Ride
	_ = json.Unmarshal(createResp.Payload, &ride)

	bookResp := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{
				RideID:    ride.ID,
				SegmentID: ride.Segments[0].ID,
				CompanyID: "empresa-a",
			},
		},
	}), passengerSession)
	var res protocol.ReservationResponse
	_ = json.Unmarshal(bookResp.Payload, &res)

	// Motorista reporta ausência (No-Show)
	noShowResp := srv.handleReportNoShow(mustJSON(protocol.ReportNoShowRequest{
		RideID:        ride.ID,
		ReservationID: res.ID,
		PassengerID:   passengerSession.UserID,
		Comment:       "Passageiro nao apareceu no ponto combinado.",
	}), driverSession)
	if noShowResp.Success != "success" {
		t.Fatalf("erro ao reportar no-show: %s", noShowResp.Message)
	}

	// Verifica se a reputação do passageiro reflete a ausência
	paxRepResp := srv.handleGetUserReputation(mustJSON(protocol.GetUserReputationRequest{
		UserID: passengerSession.UserID,
	}), driverSession)
	var paxRep models.UserReputation
	_ = json.Unmarshal(paxRepResp.Payload, &paxRep)

	if paxRep.NoShowCount != 1 {
		t.Fatalf("esperado no_show_count=1, obtido %d", paxRep.NoShowCount)
	}

	// Verifica se o status da carona atualizou para COMPLETED_NO_SHOW
	updatedRide, _ := srv.repository.GetRideByID(ride.ID)
	if updatedRide.GetStatus() != models.RideStatusCompletedNoShow {
		t.Fatalf("esperado status COMPLETED_NO_SHOW, obtido: %s", updatedRide.GetStatus())
	}
}

func TestFinishSegmentAndEarlyRating(t *testing.T) {
	srv := setupTestServer(t)
	driverSession := registerDriverAndVehicle(t, srv, "driver4@vaijunto.test")
	passengerSession := registerPassenger(t, srv, "pax3@vaijunto.test")

	depAt := time.Now().Add(1 * time.Hour).Truncate(time.Minute)
	midAt := depAt.Add(1 * time.Hour)
	endAt := midAt.Add(1 * time.Hour)

	// Carona com 2 trechos contínuos: SSA -> FSA e FSA -> Alagoinhas
	createResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depAt,
		Segments: []protocol.CreateStageRequest{
			{
				Origin:         enum.Salvador,
				Destination:    enum.FeiraDeSantana,
				DepartureAt:    depAt,
				ArrivalAt:      midAt,
				PriceCents:     3500,
				AvailableSeats: 3,
			},
			{
				Origin:         enum.FeiraDeSantana,
				Destination:    enum.Alagoinhas,
				DepartureAt:    midAt.Add(15 * time.Minute),
				ArrivalAt:      endAt,
				PriceCents:     2500,
				AvailableSeats: 3,
			},
		},
	}), driverSession)
	if createResp.Success != "success" {
		t.Fatalf("falha ao criar carona multitrecho: %s", createResp.Message)
	}

	var ride models.Ride
	_ = json.Unmarshal(createResp.Payload, &ride)
	seg1 := ride.Segments[0]
	seg2 := ride.Segments[1]

	// Passageiro reserva apenas o Trecho 1 (SSA -> FSA)
	bookResp := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{
				RideID:    ride.ID,
				SegmentID: seg1.ID,
				CompanyID: "empresa-a",
			},
		},
	}), passengerSession)
	if bookResp.Success != "success" {
		t.Fatalf("falha ao reservar trecho 1: %s", bookResp.Message)
	}

	var res protocol.ReservationResponse
	_ = json.Unmarshal(bookResp.Payload, &res)

	// 1. Passageiro tenta avaliar antes da conclusão do trecho -> DEVE FALHAR
	rateEarly := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res.ID,
		DriverID:      driverSession.UserID,
		Score:         5,
		Comment:       "Tentativa prematura",
	}), passengerSession)
	if rateEarly.Success != "error" {
		t.Fatalf("esperava erro ao avaliar trecho nao finalizado")
	}

	// 2. Motorista conclui o Trecho 1 (SSA -> FSA)
	finishSeg1Resp := srv.handleFinishSegment(mustJSON(protocol.FinishSegmentRequest{
		RideID:    ride.ID,
		SegmentID: seg1.ID,
	}), driverSession)
	if finishSeg1Resp.Success != "success" {
		t.Fatalf("falha ao finalizar trecho 1: %s", finishSeg1Resp.Message)
	}

	// A carona agora deve estar IN_PROGRESS (pois ainda falta o Trecho 2)
	updatedRide, _ := srv.repository.GetRideByID(ride.ID)
	if updatedRide.GetStatus() != models.RideStatusInProgress {
		t.Fatalf("esperado status IN_PROGRESS após 1 de 2 trechos, obtido: %s", updatedRide.GetStatus())
	}
	if updatedRide.Segments[0].GetStatus() != models.StageStatusCompleted {
		t.Fatalf("trecho 1 deveria estar COMPLETED")
	}

	// 3. Passageiro que viajou no Trecho 1 já consegue avaliar com sucesso!
	rateResp := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res.ID,
		DriverID:      driverSession.UserID,
		Score:         5,
		Comment:       "Cheguei em Feira com tranquilidade!",
	}), passengerSession)
	if rateResp.Success != "success" {
		t.Fatalf("passageiro deveria conseguir avaliar apos o seu trecho ter sido concluido: %s", rateResp.Message)
	}

	// 4. Motorista conclui o Trecho 2 (FSA -> Alagoinhas)
	finishSeg2Resp := srv.handleFinishSegment(mustJSON(protocol.FinishSegmentRequest{
		RideID:    ride.ID,
		SegmentID: seg2.ID,
	}), driverSession)
	if finishSeg2Resp.Success != "success" {
		t.Fatalf("falha ao finalizar trecho 2: %s", finishSeg2Resp.Message)
	}

	// Como todos os trechos foram finalizados, a carona inteira deve virar COMPLETED!
	finalRide, _ := srv.repository.GetRideByID(ride.ID)
	if finalRide.GetStatus() != models.RideStatusCompleted {
		t.Fatalf("esperado status COMPLETED para a carona apos todos os trechos serem concluidos, obtido: %s", finalRide.GetStatus())
	}

	// 5. Tentar finalizar o Trecho 1 novamente -> DEVE FALHAR
	finishSeg1Again := srv.handleFinishSegment(mustJSON(protocol.FinishSegmentRequest{
		RideID:    ride.ID,
		SegmentID: seg1.ID,
	}), driverSession)
	if finishSeg1Again.Success != "error" {
		t.Fatalf("esperava erro ao tentar finalizar trecho ja concluido")
	}
}
