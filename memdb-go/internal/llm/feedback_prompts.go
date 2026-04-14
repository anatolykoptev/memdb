package llm

// feedback_prompts.go — EN-only prompts for feedback pipeline.
// Ported from Python mem_feedback_prompts.py.

const keywordReplacePrompt = `**Instruction:**
Please analyze the user's input text to determine if it is a "keyword replacement" request. If yes, follow these steps:

1.  **Identify the request type**: Confirm whether the user is asking to replace a specific word or phrase with another **within a specified scope**.
2.  **Extract the modification scope**: Determine the scope where the modification should apply.
 - If the user mentions a specific **document, file, or material identifier**, extract this description as the document scope.
 - **If the user does not explicitly specify any scope, mark the scope as "NONE"**.
3.  **Extract the original term (A)**: Identify the original word or phrase the user wants to be replaced.
4.  **Extract the target term (B)**: Identify the target word or phrase the user wants to replace it with.

**Output JSON Format**:
{
    "if_keyword_replace": "true" | "false",
    "doc_scope": "[Extracted specific file or document description]" | "NONE" | null,
    "original": "[Extracted original word or phrase A]" | null,
    "target": "[Extracted target word or phrase B]" | null
}
- **If it is NOT a replacement request**, set ` + "`if_keyword_replace`" + ` to "false", and set the values for doc_scope, original, and target to null.
- **If it IS a replacement request**, set ` + "`if_keyword_replace`" + ` to "true" and fill in the remaining fields.

**User Input**
%s

**Output**:`

//nolint:lll
const feedbackJudgementPrompt = `You are an answer quality analysis expert. Please strictly follow the steps and criteria below to analyze the provided "User and Assistant Chat History" and "User Feedback," and fill the final evaluation results into the specified JSON format.

Analysis Steps and Criteria:
1. *Validity Judgment*:
 - Valid (true): The content of the user's feedback is related to the topic, task, or the assistant's last response in the chat history.
 - Invalid (false): The user's feedback is entirely unrelated to the conversation history.

2. *User Attitude Judgment*:
 - Dissatisfied: The feedback shows negative emotions, such as pointing out errors, expressing confusion, or stating the problem remains unsolved.
 - Satisfied: The feedback shows positive emotions, such as expressing thanks or giving praise.
 - Irrelevant: The content of the feedback is unrelated to evaluating the assistant's answer.

3. *Summary Information Generation* (corrected_info field):
 - Generate a concise list of factual statements that summarize the core information from the user's feedback.
 - When the feedback provides corrections, focus only on the corrected information.
 - When the feedback provides supplements, integrate all valid information (both old and new).
 - Keep any relevant time information and express as concrete dates or periods.
 - Focus on statement of objective facts.

Output Format:
[
    {
        "validity": "<string, 'true' or 'false'>",
        "user_attitude": "<string, 'dissatisfied' or 'satisfied' or 'irrelevant'>",
        "corrected_info": "<string, factual information records written in English>",
        "key": "<string, concise memory title in English (2-5 words)>",
        "tags": "<list of relevant keywords in English for retrieval>"
    }
]

Dialogue History:
{chat_history}

feedback time: {feedback_time}

User Feedback:
{user_feedback}

Output:`

//nolint:lll
const updateFormerMemoriesPrompt = `Operation recommendations:
Please analyze the newly acquired factual information and determine how this information should be updated to the memory database: add, update, or keep unchanged, and provide final operation recommendations.
You must strictly return the response in the following JSON format:

{
    "operations":
        [
            {
                "id": "<memory ID>",
                "text": "<memory content>",
                "operation": "<operation type, must be one of 'ADD', 'UPDATE', 'NONE'>",
                "old_memory": "<original memory content, required only when operation is 'UPDATE'>"
            }
        ]
}

*Requirements*:
1. If the new fact does not provide additional information, set operation to "NONE."
2. If the new fact is more accurate or requires correction for the same entity, set operation to "UPDATE".
3. If the new fact contradicts existing memory in key information, update and set operation to "UPDATE", only modifying relevant error segments.
4. If no existing memory requires updating or the new fact refers to a different entity, set operation to "ADD."
5. Facts about different entities in the same time period can coexist.

*ID Management Rules*:
- Update operation: Keep the original ID unchanged
- Add operation: Generate a new unique ID in the format of a 4-digit string (e.g., "0001")

*Important Requirements*:
1. For "UPDATE" operations, provide the old_memory field
2. Compare existing memories one by one, do not omit any content requiring updates
3. "text" should record the final adopted memory
4. Both text and old_memory content should be in English
5. Return only the JSON format response

**Current time**
{now_time}

**Current Memories**
{current_memories}

**The background of the new fact being put forward**
{chat_history}

**Newly facts**
{new_facts}

Operation recommendations:`

//nolint:lll
const updateJudgementPrompt = `# Batch UPDATE Safety Assessment Instruction

**Background**:
This instruction serves as a supplementary safety verification layer for the memory update instruction. It evaluates each UPDATE operation to ensure safety and effectiveness.

**Safety Assessment Process (for each UPDATE entry)**:
1. **Entity Consistency Check**: Verify that old and new texts describe the same core entity.
2. **Semantic Relevance Check**: Determine whether the new information directly corrects or supplements the old information.
3. **Context Preservation Check**: Ensure only parts needing correction are modified.

**Key Decision Rules**:
1. If core entities are different → "INVALID"
2. If core entities are the same but information is unrelated → "NONE"
3. If all three checks pass → "UPDATE_APPROVED"

**Output Format**:
{
    "operations_judgement": [
        {
            "id": "...",
            "text": "...",
            "old_memory": "...",
            "judgement": "INVALID" | "NONE" | "UPDATE_APPROVED"
        }
    ]
}

Input operations list:
{raw_operations}

Safety assessment output:`
