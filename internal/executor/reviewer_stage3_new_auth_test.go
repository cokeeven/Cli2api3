package executor

import (
 "testing"
 "time"
 "github.com/caigee-cmd/cli2api/internal/accounts"
)
func TestReviewerProbeSuccessAfterNewAuthFailurePreservesModelGate(t *testing.T) {
 p:=NewPool(nil,nil)
 p.SetAuthCoordinator(&accounts.AuthCoordinator{})
 p.Upsert(Item{ID:"a",Provider:"workbuddy",Region:"cn",Models:[]string{"m"},ModelDownUntil:map[string]time.Time{"m":time.Now().Add(-time.Second)}})
 probe,release,ok:=p.acquireRoute(RouteQuery{ProviderFilter:"workbuddy",RegionFilter:"cn",PublicModel:"m"},routingIdentity{})
 if !ok || !probe.modelProbe {t.Fatal("probe was not admitted")}; defer release()
 p.MarkClassified("a",Classified{Kind:accounts.KindAuth,Scope:"account",Cooldown:time.Minute,Status:401})
 p.MarkProbeOK(probe,"m")
 item,_:=p.ByID("a")
 if item.LastKind!=accounts.KindAuth || item.ModelDownUntil["m"].IsZero(){t.Fatalf("stale probe removed newer auth or model gate: auth=%s model=%v",item.LastKind,item.ModelDownUntil)}
}
