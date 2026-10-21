------------------------- MODULE AcceptedSteering -------------------------
EXTENDS Naturals

CONSTANTS RequestCount, MaxCrashes, FenceBeforeSend
Requests == 1..RequestCount

VARIABLES durable, deliveries, nextRequest, phase, current, crashes
vars == <<durable, deliveries, nextRequest, phase, current, crashes>>

Init ==
    /\ durable = [r \in Requests |-> "unaccepted"]
    /\ deliveries = [r \in Requests |-> 0]
    /\ nextRequest = 1
    /\ phase = "idle"
    /\ current = 0
    /\ crashes = 0

\* Service.Accept commits one request. Queue order follows commit order.
Accept ==
    /\ phase \notin {"down", "recover"}
    /\ nextRequest \in Requests
    /\ durable' = [durable EXCEPT ![nextRequest] = "pending"]
    /\ nextRequest' = nextRequest + 1
    /\ UNCHANGED <<deliveries, phase, current, crashes>>

\* One dispatcher owns this Runtime Owner. Resolution succeeds in this scope.
SelectPending ==
    /\ phase = "idle"
    /\ \E r \in Requests:
        /\ durable[r] = "pending"
        /\ \A earlier \in 1..(r-1): durable[earlier] # "pending"
        /\ current' = r
    /\ phase' = IF FenceBeforeSend THEN "fence" ELSE "send"
    /\ UNCHANGED <<durable, deliveries, nextRequest, crashes>>

\* MarkDispatchStarted returns only after its SQLite transaction commits.
Fence ==
    /\ phase = "fence"
    /\ durable' = [durable EXCEPT ![current] = "dispatch_started"]
    /\ phase' = IF FenceBeforeSend THEN "send" ELSE "settle"
    /\ UNCHANGED <<deliveries, nextRequest, current, crashes>>

\* The external Runtime receives the request. This is NOT a database commit.
Send ==
    /\ phase = "send"
    /\ deliveries' = [deliveries EXCEPT ![current] = @ + 1]
    /\ phase' = IF FenceBeforeSend THEN "settle" ELSE "fence"
    /\ UNCHANGED <<durable, nextRequest, current, crashes>>

\* MarkApplied commits the outcome. Other terminal outcomes are out of scope.
Settle ==
    /\ phase = "settle"
    /\ durable' = [durable EXCEPT ![current] = "applied"]
    /\ phase' = "idle"
    /\ current' = 0
    /\ UNCHANGED <<deliveries, nextRequest, crashes>>

\* A crash loses the dispatcher, not committed rows or external deliveries.
Crash ==
    /\ phase # "down"
    /\ crashes < MaxCrashes
    /\ phase' = "down"
    /\ current' = 0
    /\ crashes' = crashes + 1
    /\ UNCHANGED <<durable, deliveries, nextRequest>>

Restart ==
    /\ phase = "down"
    /\ phase' = "recover"
    /\ UNCHANGED <<durable, deliveries, nextRequest, current, crashes>>

\* recoverAcceptedSteering settles each ambiguous row in a separate transaction.
RecoverFenced ==
    /\ phase = "recover"
    /\ \E r \in Requests:
        /\ durable[r] = "dispatch_started"
        /\ durable' = [durable EXCEPT ![r] = "action_required"]
    /\ UNCHANGED <<deliveries, nextRequest, phase, current, crashes>>

ResumeDispatch ==
    /\ phase = "recover"
    /\ \A r \in Requests: durable[r] # "dispatch_started"
    /\ phase' = "idle"
    /\ UNCHANGED <<durable, deliveries, nextRequest, current, crashes>>

Next == Accept \/ SelectPending \/ Fence \/ Send \/ Settle
        \/ Crash \/ Restart \/ RecoverFenced \/ ResumeDispatch

NoReplay == \A r \in Requests: deliveries[r] <= 1
=============================================================================
