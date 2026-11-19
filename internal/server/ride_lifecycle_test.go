package server

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
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
	passenger1 := registerPassenger(t, srv, "pax2@vaijunto.test")
	passenger2 := registerPassenger(t, srv, "pax2b@vaijunto.test")
	outsider := registerPassenger(t, srv, "outsider@vaijunto.test")

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

	bookResp1 := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{
				RideID:    ride.ID,
				SegmentID: ride.Segments[0].ID,
				CompanyID: "empresa-a",
			},
		},
	}), passenger1)
	var res1 protocol.ReservationResponse
	_ = json.Unmarshal(bookResp1.Payload, &res1)

	bookResp2 := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{
			{
				RideID:    ride.ID,
				SegmentID: ride.Segments[0].ID,
				CompanyID: "empresa-a",
			},
		},
	}), passenger2)
	var res2 protocol.ReservationResponse
	_ = json.Unmarshal(bookResp2.Payload, &res2)

	// 1. Tentar reportar no-show para quem NÃO reservou a carona -> DEVE FALHAR
	outsiderResp := srv.handleReportNoShow(mustJSON(protocol.ReportNoShowRequest{
		RideID:      ride.ID,
		PassengerID: outsider.UserID,
		Comment:     "Nao reservou",
	}), driverSession)
	if outsiderResp.Success != "error" {
		t.Fatalf("esperava erro ao reportar no-show de não-passageiro")
	}

	// 2. Motorista inicia a carona e reporta ausência do passageiro 1
	_ = srv.handleStartRide(mustJSON(protocol.StartRideRequest{RideID: ride.ID}), driverSession)
	noShowResp := srv.handleReportNoShow(mustJSON(protocol.ReportNoShowRequest{
		RideID:        ride.ID,
		ReservationID: res1.ID,
		PassengerID:   passenger1.UserID,
		Comment:       "Passageiro nao apareceu no ponto combinado.",
	}), driverSession)
	if noShowResp.Success != "success" {
		t.Fatalf("erro ao reportar no-show: %s", noShowResp.Message)
	}

	// 3. Reportar no-show duplicado para o mesmo passageiro -> DEVE FALHAR
	noShowDup := srv.handleReportNoShow(mustJSON(protocol.ReportNoShowRequest{
		RideID:        ride.ID,
		ReservationID: res1.ID,
		PassengerID:   passenger1.UserID,
		Comment:       "Duplicado",
	}), driverSession)
	if noShowDup.Success != "error" {
		t.Fatalf("esperava erro ao tentar duplicar no-show")
	}

	// 4. Carona NÃO travou: continua IN_PROGRESS e pode ser concluída para o passageiro 2!
	midRide, _ := srv.repository.GetRideByID(ride.ID)
	if midRide.GetStatus() != models.RideStatusInProgress {
		t.Fatalf("esperado status IN_PROGRESS após no-show de 1 dos passageiros, obtido: %s", midRide.GetStatus())
	}

	finishResp := srv.handleFinishRide(mustJSON(protocol.FinishRideRequest{RideID: ride.ID}), driverSession)
	if finishResp.Success != "success" {
		t.Fatalf("carona deveria poder ser concluída para os demais passageiros: %s", finishResp.Message)
	}

	// 5. Passageiro 1 (que levou no-show) NÃO pode avaliar o motorista
	rateAfterNoShow := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res1.ID,
		DriverID:      driverSession.UserID,
		Score:         1,
		Comment:       "Quero avaliar mesmo faltando",
	}), passenger1)
	if rateAfterNoShow.Success != "error" {
		t.Fatalf("passageiro com no-show não deveria conseguir avaliar o motorista")
	}

	// 6. Passageiro 2 (que viajou) consegue avaliar normalmente
	ratePax2 := srv.handleRateDriver(mustJSON(protocol.RateDriverRequest{
		ReservationID: res2.ID,
		DriverID:      driverSession.UserID,
		Score:         5,
		Comment:       "Viagem normal para o segundo passageiro",
	}), passenger2)
	if ratePax2.Success != "success" {
		t.Fatalf("passageiro 2 deveria conseguir avaliar: %s", ratePax2.Message)
	}

	// 7. Verifica se a reputação do passageiro 1 reflete a ausência
	paxRepResp := srv.handleGetUserReputation(mustJSON(protocol.GetUserReputationRequest{
		UserID: passenger1.UserID,
	}), driverSession)
	var paxRep models.UserReputation
	_ = json.Unmarshal(paxRepResp.Payload, &paxRep)

	if paxRep.NoShowCount != 1 {
		t.Fatalf("esperado no_show_count=1, obtido %d", paxRep.NoShowCount)
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

func TestRatePassengerValidatesBookingAndSegmentCompletion(t *testing.T) {
	srv := setupTestServer(t)
	driverSession := registerDriverAndVehicle(t, srv, "driver5@vaijunto.test")
	passengerSession := registerPassenger(t, srv, "pax5@vaijunto.test")
	outsider := registerPassenger(t, srv, "outsider5@vaijunto.test")

	depAt := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	midAt := depAt.Add(1 * time.Hour)
	endAt := midAt.Add(1 * time.Hour)

	createResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depAt,
		Segments: []protocol.CreateStageRequest{
			{Origin: enum.Salvador, Destination: enum.FeiraDeSantana, DepartureAt: depAt, ArrivalAt: midAt, PriceCents: 3500, AvailableSeats: 3},
			{Origin: enum.FeiraDeSantana, Destination: enum.Alagoinhas, DepartureAt: midAt.Add(10 * time.Minute), ArrivalAt: endAt, PriceCents: 2000, AvailableSeats: 3},
		},
	}), driverSession)
	var ride models.Ride
	_ = json.Unmarshal(createResp.Payload, &ride)

	srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{{RideID: ride.ID, SegmentID: ride.Segments[0].ID, CompanyID: "empresa-a"}},
	}), passengerSession)

	// Conclui apenas o Trecho 1 (carona ainda em IN_PROGRESS)
	srv.handleFinishSegment(mustJSON(protocol.FinishSegmentRequest{RideID: ride.ID, SegmentID: ride.Segments[0].ID}), driverSession)

	// 1. Não-passageiro não pode ser avaliado -> DEVE FALHAR
	rateOutsider := srv.handleRatePassenger(mustJSON(protocol.RatePassengerRequest{
		RideID:      ride.ID,
		PassengerID: outsider.UserID,
		Score:       5,
		Comment:     "Tentativa indevida",
	}), driverSession)
	if rateOutsider.Success != "error" {
		t.Fatalf("esperava erro ao avaliar usuário sem reserva confirmada")
	}

	// 2. Passageiro do Trecho 1 já pode ser avaliado após conclusão do seu trecho
	ratePax := srv.handleRatePassenger(mustJSON(protocol.RatePassengerRequest{
		RideID:      ride.ID,
		PassengerID: passengerSession.UserID,
		Score:       5,
		Comment:     "Pontual no primeiro trecho",
	}), driverSession)
	if ratePax.Success != "success" {
		t.Fatalf("esperava sucesso ao avaliar passageiro após trecho concluído: %s", ratePax.Message)
	}
}

func TestComplaintsAndCancellationRules(t *testing.T) {
	srv := setupTestServer(t)
	driverSession := registerDriverAndVehicle(t, srv, "driver6@vaijunto.test")
	passengerSession := registerPassenger(t, srv, "pax6@vaijunto.test")
	outsider := registerPassenger(t, srv, "outsider6@vaijunto.test")

	// Carona partindo em 30 min (cancelamento pelo passageiro gera LATE_CANCELLATION)
	depSoon := time.Now().Add(30 * time.Minute).Truncate(time.Minute)
	createSoonResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depSoon,
		Segments: []protocol.CreateStageRequest{
			{Origin: enum.Salvador, Destination: enum.FeiraDeSantana, DepartureAt: depSoon, ArrivalAt: depSoon.Add(1 * time.Hour), PriceCents: 3500, AvailableSeats: 3},
		},
	}), driverSession)
	var rideSoon models.Ride
	_ = json.Unmarshal(createSoonResp.Payload, &rideSoon)

	bookSoon := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{{RideID: rideSoon.ID, SegmentID: rideSoon.Segments[0].ID, CompanyID: "empresa-a"}},
	}), passengerSession)
	var resSoon protocol.ReservationResponse
	_ = json.Unmarshal(bookSoon.Payload, &resSoon)

	// 1. Não-participante tenta denunciar motorista -> DEVE FALHAR
	outsiderComplaint := srv.handleFileComplaint(mustJSON(protocol.FileComplaintRequest{
		RideID:   rideSoon.ID,
		TargetID: driverSession.UserID,
		Comment:  "Denuncia falsa",
	}), outsider)
	if outsiderComplaint.Success != "error" {
		t.Fatalf("usuário sem vínculo não deveria conseguir denunciar")
	}

	// 2. Passageiro denuncia motorista com vínculo comprovado -> SUCESSO
	paxComplaint := srv.handleFileComplaint(mustJSON(protocol.FileComplaintRequest{
		ReservationID: resSoon.ID,
		RideID:        rideSoon.ID,
		TargetID:      driverSession.UserID,
		Comment:       "Comportamento inadequado.",
	}), passengerSession)
	if paxComplaint.Success != "success" {
		t.Fatalf("falha ao registrar denúncia: %s", paxComplaint.Message)
	}

	// 3. Denúncia duplicada -> DEVE FALHAR
	paxComplaintDup := srv.handleFileComplaint(mustJSON(protocol.FileComplaintRequest{
		ReservationID: resSoon.ID,
		RideID:        rideSoon.ID,
		TargetID:      driverSession.UserID,
		Comment:       "Outra denuncia",
	}), passengerSession)
	if paxComplaintDup.Success != "error" {
		t.Fatalf("denúncia duplicada deveria falhar")
	}

	// 4. Cancelamento tardio (< 2h da partida) registra LATE_CANCELLATION na reputação do passageiro
	cancelSoon := srv.handleCancelReservation(mustJSON(protocol.CancelReservationRequest{
		ReservationID: resSoon.ID,
	}), passengerSession)
	if cancelSoon.Success != "success" {
		t.Fatalf("cancelamento tardio deveria ser aceito: %s", cancelSoon.Message)
	}

	// 5. Segunda carona com reserva: inicia e tenta cancelar -> bloqueado; e cancelamento antes do início pelo motorista gera DRIVER_CANCELLATION
	depLater := time.Now().Add(4 * time.Hour).Truncate(time.Minute)
	createLaterResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depLater,
		Segments: []protocol.CreateStageRequest{
			{Origin: enum.Salvador, Destination: enum.FeiraDeSantana, DepartureAt: depLater, ArrivalAt: depLater.Add(1 * time.Hour), PriceCents: 3500, AvailableSeats: 3},
		},
	}), driverSession)
	var rideLater models.Ride
	_ = json.Unmarshal(createLaterResp.Payload, &rideLater)

	bookLater := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{{RideID: rideLater.ID, SegmentID: rideLater.Segments[0].ID, CompanyID: "empresa-a"}},
	}), passengerSession)
	var resLater protocol.ReservationResponse
	_ = json.Unmarshal(bookLater.Payload, &resLater)

	// Motorista cancela a carona que tinha passageiro -> gera DRIVER_CANCELLATION
	cancelRideResp := srv.handleCancelRide(mustJSON(protocol.CancelRideRequest{RideID: rideLater.ID}), driverSession)
	if cancelRideResp.Success != "success" {
		t.Fatalf("erro ao cancelar carona: %s", cancelRideResp.Message)
	}

	// Terceira carona iniciada (IN_PROGRESS): não deve aparecer na busca e não pode ser cancelada
	createStartedResp := srv.handleCreateRide(mustJSON(protocol.CreateRideRequest{
		DepartureAt: depLater,
		Segments: []protocol.CreateStageRequest{
			{Origin: enum.Salvador, Destination: enum.FeiraDeSantana, DepartureAt: depLater, ArrivalAt: depLater.Add(1 * time.Hour), PriceCents: 3500, AvailableSeats: 3},
		},
	}), driverSession)
	var rideStarted models.Ride
	_ = json.Unmarshal(createStartedResp.Payload, &rideStarted)

	bookStarted := srv.handleConfirmReservation(mustJSON(protocol.ConfirmReservationRequest{
		Segments: []models.ReservedSegment{{RideID: rideStarted.ID, SegmentID: rideStarted.Segments[0].ID, CompanyID: "empresa-a"}},
	}), passengerSession)
	var resStarted protocol.ReservationResponse
	_ = json.Unmarshal(bookStarted.Payload, &resStarted)

	srv.handleStartRide(mustJSON(protocol.StartRideRequest{RideID: rideStarted.ID}), driverSession)

	// Busca por data NÃO deve retornar carona IN_PROGRESS
	activeRides, _ := srv.repository.GetActiveRidesByDate(depLater)
	for _, r := range activeRides {
		if r.ID == rideStarted.ID {
			t.Fatalf("carona IN_PROGRESS não deveria ser retornada em GetActiveRidesByDate")
		}
	}

	// Cancelar reserva ou carona após início -> DEVE FALHAR
	if resp := srv.handleCancelReservation(mustJSON(protocol.CancelReservationRequest{ReservationID: resStarted.ID}), passengerSession); resp.Success != "error" {
		t.Fatalf("esperava erro ao cancelar reserva de carona já iniciada")
	}
	if resp := srv.handleCancelRide(mustJSON(protocol.CancelRideRequest{RideID: rideStarted.ID}), driverSession); resp.Success != "error" {
		t.Fatalf("esperava erro ao cancelar carona já iniciada")
	}

	// Verifica contadores na reputação de passageiro e motorista
	paxRepResp := srv.handleGetUserReputation(mustJSON(protocol.GetUserReputationRequest{UserID: passengerSession.UserID}), passengerSession)
	var paxRep models.UserReputation
	_ = json.Unmarshal(paxRepResp.Payload, &paxRep)
	if paxRep.LateCancelledCount != 1 {
		t.Fatalf("esperado late_cancelled_count=1, obtido %d", paxRep.LateCancelledCount)
	}

	drvRepResp := srv.handleGetUserReputation(mustJSON(protocol.GetUserReputationRequest{UserID: driverSession.UserID}), passengerSession)
	var drvRep models.UserReputation
	_ = json.Unmarshal(drvRepResp.Payload, &drvRep)
	if drvRep.ComplaintCount != 1 || drvRep.DriverCancelledCount != 1 {
		t.Fatalf("esperado complaint_count=1 e driver_cancelled_count=1, obtido complaint=%d driver_cancel=%d",
			drvRep.ComplaintCount, drvRep.DriverCancelledCount)
	}
}

func TestConcurrentRepositoryWritesAndPersistenceNoRace(t *testing.T) {
	usersPath := filepath.Join(t.TempDir(), "data", "users.json")
	repo, err := NewRepository(usersPath)
	if err != nil {
		t.Fatalf("criar repositório: %v", err)
	}

	const iterations = 15
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 4*iterations)

	baseDep := time.Now().Add(24 * time.Hour).Truncate(time.Minute)

	// Goroutine 1: SaveUser
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			err := repo.SaveUser(&models.User{
				ID:           uuid.New(),
				Name:         fmt.Sprintf("User %d", i),
				Email:        fmt.Sprintf("concurrent-%d@vaijunto.test", i),
				PasswordHash: "hash",
				Role:         models.RolePassenger,
			})
			if err != nil {
				errs <- fmt.Errorf("SaveUser[%d]: %w", i, err)
			}
		}
	}()

	// Goroutine 2: SaveRide
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			dep := baseDep.Add(time.Duration(i) * time.Hour)
			err := repo.SaveRide(&models.Ride{
				ID:          uuid.New(),
				DriverID:    uuid.New(),
				CompanyID:   "empresa-a",
				DepartureAt: dep,
				Segments: []models.Stage{
					{
						ID:             uuid.New(),
						Origin:         enum.Salvador,
						Destination:    enum.FeiraDeSantana,
						DepartureAt:    dep,
						ArrivalAt:      dep.Add(1 * time.Hour),
						PriceCents:     2500,
						AvailableSeats: 3,
					},
				},
			})
			if err != nil {
				errs <- fmt.Errorf("SaveRide[%d]: %w", i, err)
			}
		}
	}()

	// Goroutine 3: SaveSegmentBooking
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			err := repo.SaveSegmentBooking(&models.SegmentBooking{
				ID:          uuid.New(),
				TxID:        fmt.Sprintf("tx-race-%d", i),
				RideID:      uuid.New(),
				SegmentID:   uuid.New(),
				PassengerID: uuid.New(),
				CompanyID:   "empresa-b",
			})
			if err != nil {
				errs <- fmt.Errorf("SaveSegmentBooking[%d]: %w", i, err)
			}
		}
	}()

	// Goroutine 4: SaveRating
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			err := repo.SaveRating(&models.Rating{
				ID:       uuid.New(),
				RideID:   uuid.New(),
				AuthorID: uuid.New(),
				TargetID: uuid.New(),
				Type:     models.RatingTypePassengerToDriver,
				Score:    5,
				Comment:  fmt.Sprintf("Avaliação concorrente %d", i),
			})
			if err != nil {
				errs <- fmt.Errorf("SaveRating[%d]: %w", i, err)
			}
		}
	}()

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
}
