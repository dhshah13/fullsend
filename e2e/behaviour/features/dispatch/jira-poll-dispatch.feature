Feature: Jira poll dispatch

  The Jira poll input driver discovers events on Jira issues and produces
  dispatch records that trigger harness agent workflows. These scenarios
  run the real poller against a local mock Jira server, then assert on
  the dispatch records written to the output file.

  Unlike the GitHub dispatch scenarios, this does not exercise workflow
  dispatch or artifact collection — it validates the poll-to-dispatch-
  record path in isolation.

  Background:
    Given a mock Jira server

  Scenario: Jira comment with slash command produces triage dispatch
    Given a Jira issue "PROJ-101" with labels "bug"
    When a comment "/fs-triage check acceptance criteria" is added to Jira issue "PROJ-101"
    And the Jira poller runs
    Then the dispatch output contains a "triage" stage for issue "PROJ-101"

  Scenario: Jira label change produces dispatch for label-triggered harness
    Given a Jira issue "PROJ-202" with labels "backlog"
    When the label "ready-to-code" is added to Jira issue "PROJ-202"
    And the Jira poller runs
    Then the dispatch output contains a "code" stage for issue "PROJ-202"
    And the dispatch output does not contain a stage for issue "PROJ-999"

  Scenario: Jira comment from a group-backed role member resolves the correct role
    Given a Jira issue "PROJ-303" with labels "bug"
    And the "Administrators" Jira project role is backed by group "admin-group-001"
    And Jira user "admin-001" belongs to group "admin-group-001"
    When a comment "/fs-triage escalate" from Jira user "admin-001" is added to Jira issue "PROJ-303"
    And the Jira poller runs
    Then the dispatch output contains a "triage" stage for issue "PROJ-303"
    And the dispatch output attributes issue "PROJ-303" to actor "admin-001" with role "admin"
