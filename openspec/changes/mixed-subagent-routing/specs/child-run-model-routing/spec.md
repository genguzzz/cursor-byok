## ADDED Requirements

### Requirement: Child run uses subagent model override for routing
When a `run_request` or `prewarm_request` has a non-empty `subagent_type_name` and a matching `subagent_model_overrides` entry with `selection=model`, the backend MUST treat that override model id as the run's requested model for mixed routing.

#### Scenario: Official parent with local explore child
- **WHEN** child `subagent_type_name` is `explore` and override model id is a configured channel hash
- **THEN** that `request_id` MUST route to the local forwarder
- **THEN** the BidiAppend body forwarded/handled MUST carry the override as `requested_model`

#### Scenario: Local parent with official explore child
- **WHEN** child `subagent_type_name` is `explore` and override model id is a non-channel official model name
- **THEN** that `request_id` MUST route upstream
- **THEN** the upstream request body MUST use the official model id as `requested_model`

#### Scenario: Inherit leaves parent model
- **WHEN** child override selection is `inherit`
- **THEN** routing MUST continue to use the wire `requested_model`

#### Scenario: Parent run ignores explore override for its own model
- **WHEN** `subagent_type_name` is empty
- **THEN** `subagent_model_overrides` MUST NOT rewrite the parent `requested_model`
