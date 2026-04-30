package search

// answer_enhance_anchors.go — multilingual anchor questions used to build the
// per-category centroids in answer_enhance_classifier.go.
//
// Concern split: the anchor data set is curated content that shifts every
// classification result on edit. Keeping it in its own file makes that
// editorial surface obvious and isolates the classifier logic from its data.
//
// Curation rules (enforced by TestAnchorsCoverAllCategoriesAndLangs):
//   - ≥5 anchors per category
//   - ≥1 anchor in each of {en, ru, zh}
//   - shapes lifted from LoCoMo question patterns so the centroids
//     generalise to the benchmark and to natural multilingual chat
//
// These strings ship inline (no model artifacts). Re-curate carefully —
// any change shifts every centroid and so every classification result.

// anchorQuestions is the curated multilingual anchor set used to build the
// per-category centroids in lazyEmbedClassifier.computeCentroids.
var anchorQuestions = map[QueryCategory][]string{
	QueryCategorySingleHop: {
		// en
		"How many children does Alice have?",
		"What is Bob's pet's name?",
		"Who is Carol's partner?",
		"How often does she visit?",
		"What's the count of items?",
		// ru
		"Сколько у Алисы детей?",
		"Как зовут питомца Боба?",
		"Кто партнёр Кэрол?",
		"Как часто она приезжает?",
		"Какое количество предметов?",
		// zh
		"Alice有几个孩子？",
		"Bob的宠物叫什么名字？",
		"Carol的伴侣是谁？",
		"她多久来一次？",
		"项目有多少？",
	},
	QueryCategoryMultiHop: {
		// en
		"Who did Alice meet at the conference?",
		"What did Bob say when Carol arrived?",
		"Where did they go after dinner with the kids?",
		"Which book did Alice recommend that Bob read?",
		"Who introduced Alice to Carol's family?",
		// ru
		"Кого Алиса встретила на конференции?",
		"Что Боб сказал когда Кэрол пришла?",
		"Куда они пошли после ужина с детьми?",
		"Какую книгу Алиса посоветовала Бобу?",
		"Кто познакомил Алису с семьёй Кэрол?",
		// zh
		"Alice在会议上遇到了谁？",
		"Carol到达时Bob说了什么？",
		"和孩子们吃完晚饭后他们去了哪里？",
		"Alice推荐Bob读了哪本书？",
		"谁把Alice介绍给了Carol的家人？",
	},
	QueryCategoryTemporal: {
		// en
		"When did Alice come out?",
		"What year was the meeting?",
		"How long did the trip last?",
		"What date is the birthday?",
		"When was the photo taken?",
		// ru
		"Когда Алиса призналась?",
		"В каком году была встреча?",
		"Как долго длилась поездка?",
		"Какого числа день рождения?",
		"Когда была сделана фотография?",
		// zh
		"Alice什么时候出柜的？",
		"会议是哪一年？",
		"旅行持续多久？",
		"生日是几月几号？",
		"照片是什么时候拍的？",
	},
	QueryCategoryOpenDomain: {
		// en
		"Tell me about Alice's hobbies",
		"What do you know about Bob's family?",
		"Describe Carol's career",
		"How is Alice feeling lately?",
		"What kind of person is Bob?",
		// ru
		"Расскажи об увлечениях Алисы",
		"Что ты знаешь о семье Боба?",
		"Опиши карьеру Кэрол",
		"Как Алиса чувствует себя в последнее время?",
		"Что Боб за человек?",
		// zh
		"告诉我Alice的爱好",
		"你对Bob的家人了解多少？",
		"描述Carol的职业",
		"Alice最近感觉怎么样？",
		"Bob是什么样的人？",
	},
	QueryCategoryAdversarial: {
		// en
		"Did Alice never travel to Japan?",
		"Is it true that Bob hates pets?",
		"Did Carol not finish her degree?",
		"Is Alice's claim about the photo false?",
		"Did Bob never speak to Carol?",
		// ru
		"Алиса никогда не ездила в Японию?",
		"Правда ли что Боб ненавидит животных?",
		"Не закончила ли Кэрол свою степень?",
		"Ложно ли утверждение Алисы о фотографии?",
		"Боб никогда не разговаривал с Кэрол?",
		// zh
		"Alice从未去过日本吗？",
		"Bob讨厌宠物是真的吗？",
		"Carol没有完成学位吗？",
		"Alice关于照片的说法是错误的吗？",
		"Bob从来没有跟Carol说过话吗？",
	},
}

// categoryHintStrings maps each category to a short (one-line) shape hint
// appended after the base extractor prompt. open_domain is intentionally
// empty — it has no shape constraint to add and the base prompt's general
// rules are sufficient. Empty hint suppresses the entire hint block so the
// prompt is byte-identical to the base in that case.
var categoryHintStrings = map[QueryCategory]string{
	QueryCategorySingleHop:   "Prefer single number/name/count over noun phrase. Strip articles (a/the).",
	QueryCategoryMultiHop:    "Combine evidence from multiple memories. Cite source IDs in linking order.",
	QueryCategoryTemporal:    "Format dates as YYYY-MM-DD. Format durations as 'N days/months/years'.",
	QueryCategoryOpenDomain:  "",
	QueryCategoryAdversarial: "If question's premise contradicts the memories, return the contradicting fact. If unsupported, return UNKNOWN with confidence ≤ 0.3.",
}
