module openai_example_agent

go 1.26.4

require (
	tinychain v0.0.0
	tinychain/agent v0.0.0
)

replace tinychain => ../../client

replace tinychain/agent => ../../agent
