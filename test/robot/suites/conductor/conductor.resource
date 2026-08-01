*** Settings ***
Documentation     Shared setup for the Conductor payment-switch suites: brings up
...               a Mixer + a single "rack-switch" Rack, with the auth SDL spec
...               provisioned into the Rack's working directory.
Library           fluxrigLibrary
Library           ConductorLibrary
Library           OperatingSystem

*** Variables ***
${MIXER_PORT}     8090
${SWITCH_PORT}    8583
${RACK_NAME}      rack-switch
${WORK_DIR}       ${EMPTY}

*** Keywords ***
Start Switch Platform
    [Documentation]    Force-clean, create the workspace, provision the spec, and
    ...                start Mixer + Rack. Scenario import and scheme hosts are
    ...                suite-specific and done by the caller.
    [Arguments]    ${suite_dir}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_dir}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    Set Suite Variable    ${SUITE_DIR}    ${suite_dir}
    Set Suite Variable    ${SPEC}    ${suite_dir}/specs/auth.yaml
    # The codec gears load specs/auth.yaml relative to the Rack's CWD.
    Copy File    ${suite_dir}/specs/auth.yaml    ${WORK_DIR}/rack/specs/auth.yaml
    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${suite_dir}/configs/mixer/mixer.toml    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}
    Start Rack    config_file=${suite_dir}/configs/rack/rack.toml    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=${RACK_NAME}

Deploy Switch Scenario
    [Arguments]    ${scenario_file}
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${scenario_file}
    Wait For Port    port=${SWITCH_PORT}    timeout=30
    Sleep    3s    reason=Let the uplink clients dial the schemes

Stop Switch Platform
    Run Keyword And Ignore Error    Stop All Conductor Processes
    Run Keyword And Ignore Error    Stop Process    rack
    Run Keyword And Ignore Error    Stop Process    mixer
