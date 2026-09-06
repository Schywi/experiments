------------------------------ MODULE WormScale ------------------------------
EXTENDS Naturals, Sequences

\* Bounded abstract model of controller scale reconciliation. The executable
\* check-scaling-model.sh validates this finite transition relation locally.
CONSTANT MaxReplicas

ASSUME MaxReplicas = 20

VARIABLES desiredReplicas, acceptedIntentIds

Init ==
  /\ desiredReplicas = 1
  /\ acceptedIntentIds = {}

NewIntent(intentId) ==
  /\ intentId \notin acceptedIntentIds
  /\ acceptedIntentIds' = acceptedIntentIds \cup {intentId}
  /\ desiredReplicas' = IF desiredReplicas < MaxReplicas
                         THEN desiredReplicas + 1
                         ELSE desiredReplicas

DuplicateIntent(intentId) ==
  /\ intentId \in acceptedIntentIds
  /\ acceptedIntentIds' = acceptedIntentIds
  /\ desiredReplicas' = desiredReplicas

Next == \E intentId \in Nat : NewIntent(intentId) \/ DuplicateIntent(intentId)

TypeInvariant ==
  /\ desiredReplicas \in 1..MaxReplicas
  /\ acceptedIntentIds \subseteq Nat

Idempotency ==
  \A intentId \in acceptedIntentIds :
    DuplicateIntent(intentId) => desiredReplicas' = desiredReplicas

=============================================================================
