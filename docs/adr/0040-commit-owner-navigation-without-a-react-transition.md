# 0040: Commit owner navigation without a React transition

## Status

Accepted.

## Context

React Router can change the browser URL before its React transition commits. The previous Runtime Owner workspace can remain visible and accept input during that interval. Text entered after a Session link click then belongs to the old workspace and disappears when the new workspace mounts. Owner and request fences do not solve this navigation boundary: they correctly treat the input as belonging to the still-mounted owner.

## Decision

Set `useTransitions={false}` on the application `RouterProvider`. Route identity changes must not be deferred as background React transitions. If the next route suspends, the existing loading fallback replaces the interactive old content until the next route can render.

Do not copy input between owners or infer draft ownership from the browser URL. Keep the existing owner and request fences.

## Consequences

Navigation can show a loading fallback instead of retaining the old page. The App regression holds the next Session render and checks that the old composer is no longer accessible after the URL changes. Browser tests separately check the observed click-to-input boundary under diagnostic CPU throttling.
