use cursor_server::cursor::{
    compile::compile_context,
    protocol::proto::agent::v1 as pb,
};

#[test]
fn cli_skill_descriptors_are_projected_as_available_skills() {
    let context = pb::RequestContext {
        skill_options: Some(pb::SkillOptions {
            skill_descriptors: vec![pb::SkillDescriptor {
                description: "Runs Android device automation.".into(),
                readme_file_path: "/Users/test/.cursor/skills/android-device/SKILL.md".into(),
                ..Default::default()
            }],
        }),
        ..Default::default()
    };

    let rendered = compile_context(&context, "2026-09-08");
    assert!(rendered.contains("<agent_skills>"));
    assert!(rendered.contains(
        "<agent_skill fullPath=\"/Users/test/.cursor/skills/android-device/SKILL.md\">Runs Android device automation.</agent_skill>"
    ));
}

#[test]
fn duplicate_agent_and_cli_skill_is_rendered_once() {
    let path = "/Users/test/.cursor/skills/android-device/SKILL.md";
    let context = pb::RequestContext {
        agent_skills: vec![pb::AgentSkill {
            full_path: path.into(),
            description: "Runs Android device automation.".into(),
            ..Default::default()
        }],
        skill_options: Some(pb::SkillOptions {
            skill_descriptors: vec![pb::SkillDescriptor {
                description: "Runs Android device automation.".into(),
                readme_file_path: path.into(),
                ..Default::default()
            }],
        }),
        ..Default::default()
    };

    let rendered = compile_context(&context, "2026-09-08");
    assert_eq!(rendered.matches("<agent_skill ").count(), 1);
}
