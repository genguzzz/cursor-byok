# Tasks: v0.0.49 CodeBuddy Model Import

- [x] Map `codebuddy` legacy type in `legacy_config.rs::model_input`
- [ ] Add migration `0005_allow_codebuddy_model_type.sql` relaxing the CHECK
- [ ] Add `manually_imports_v0049_codebuddy_models` test
- [ ] Run `cargo test --lib` (all green)
- [ ] Rebuild desktop `.app`, install to `/Applications`, verify import