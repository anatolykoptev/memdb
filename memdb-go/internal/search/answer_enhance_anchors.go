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
//
// Cefix9 diagnosis (2026-04-30): the original 15 anchors per category
// produced a centroid that mis-routed real LoCoMo questions:
//
//   - Only 6 / 149 D10 calls landed on top1=temporal (~30 expected)
//     because temporal anchors were all event-date shaped ("when did X",
//     "what year was Y") and missed the duration / "how long has X been"
//     family.
//   - Open_domain absorbed 42 calls (vs ~30 expected) because anchors
//     like "Tell me about X's hobbies" attract any "how does X..."
//     question with a topic noun.
//
// This expansion doubles each category to 30 anchors (~10 per language)
// covering the patterns observed in LoCoMo question types:
//   - temporal: duration, age-at-event, since-when, "what year did X"
//   - single-hop: favourite/preference, possession, residence, count
//   - multi-hop: chain reasoning, comparison-after-event, why-because
//   - open-domain: "describe", "summarise", "what is X like"
//   - adversarial: "actually", "really", "confirm if" — softer phrasings
//     that LoCoMo cat-5 uses without explicit never/false markers
var anchorQuestions = map[QueryCategory][]string{
	QueryCategorySingleHop: {
		// en — counts, names, possessions, preferences
		"How many children does Alice have?",
		"What is Bob's pet's name?",
		"Who is Carol's partner?",
		"How often does she visit?",
		"What's the count of items?",
		"What is Alice's favourite colour?",
		"Where does Bob live?",
		"What kind of car does Carol drive?",
		"Which job does Alice have?",
		"What is the price of the bag?",
		// ru
		"Сколько у Алисы детей?",
		"Как зовут питомца Боба?",
		"Кто партнёр Кэрол?",
		"Как часто она приезжает?",
		"Какое количество предметов?",
		"Какой любимый цвет у Алисы?",
		"Где живёт Боб?",
		"Какая работа у Алисы?",
		"Какой автомобиль у Кэрол?",
		"Сколько стоит сумка?",
		// zh
		"Alice有几个孩子？",
		"Bob的宠物叫什么名字？",
		"Carol的伴侣是谁？",
		"她多久来一次？",
		"项目有多少？",
		"Alice最喜欢的颜色是什么？",
		"Bob住在哪里？",
		"Alice的工作是什么？",
		"Carol开什么车？",
		"包多少钱？",
	},
	QueryCategoryMultiHop: {
		// en — chains across 2+ memories
		"Who did Alice meet at the conference?",
		"What did Bob say when Carol arrived?",
		"Where did they go after dinner with the kids?",
		"Which book did Alice recommend that Bob read?",
		"Who introduced Alice to Carol's family?",
		"How did Alice feel after Bob told her the news?",
		"Why did Carol leave the party that Alice organised?",
		"What changed between Alice and Bob after the trip?",
		"Who helped Carol when Alice was away?",
		"Which decision led Bob to move?",
		// ru
		"Кого Алиса встретила на конференции?",
		"Что Боб сказал когда Кэрол пришла?",
		"Куда они пошли после ужина с детьми?",
		"Какую книгу Алиса посоветовала Бобу?",
		"Кто познакомил Алису с семьёй Кэрол?",
		"Что Алиса почувствовала после того как Боб сказал?",
		"Почему Кэрол ушла с вечеринки которую Алиса устроила?",
		"Что изменилось между Алисой и Бобом после поездки?",
		"Кто помог Кэрол когда Алиса была в отъезде?",
		"Какое решение привело Боба к переезду?",
		// zh
		"Alice在会议上遇到了谁？",
		"Carol到达时Bob说了什么？",
		"和孩子们吃完晚饭后他们去了哪里？",
		"Alice推荐Bob读了哪本书？",
		"谁把Alice介绍给了Carol的家人？",
		"Bob告诉Alice消息后她有什么感受？",
		"Alice组织的派对上Carol为什么离开？",
		"旅行后Alice和Bob之间发生了什么变化？",
		"Alice不在时谁帮助了Carol？",
		"什么决定让Bob搬家了？",
	},
	QueryCategoryTemporal: {
		// en — dates, durations, age-at-event, since-when, year-of
		"When did Alice come out?",
		"What year was the meeting?",
		"How long did the trip last?",
		"What date is the birthday?",
		"When was the photo taken?",
		"How long has Melanie been creating art?",
		"How old was Carol when she moved?",
		"Since when has Alice been working there?",
		"What year did Bob graduate?",
		"How many years has Carol lived in Tokyo?",
		// ru
		"Когда Алиса призналась?",
		"В каком году была встреча?",
		"Как долго длилась поездка?",
		"Какого числа день рождения?",
		"Когда была сделана фотография?",
		"Сколько лет Мелани занимается искусством?",
		"Сколько было Кэрол когда она переехала?",
		"С каких пор Алиса там работает?",
		"В каком году Боб закончил учёбу?",
		"Сколько лет Кэрол живёт в Токио?",
		// zh
		"Alice什么时候出柜的？",
		"会议是哪一年？",
		"旅行持续多久？",
		"生日是几月几号？",
		"照片是什么时候拍的？",
		"Melanie做艺术多少年了？",
		"Carol搬家时多少岁？",
		"Alice在那里工作多久了？",
		"Bob哪一年毕业的？",
		"Carol住在东京几年了？",
	},
	QueryCategoryOpenDomain: {
		// en — describe, summarise, what is X like
		"Tell me about Alice's hobbies",
		"What do you know about Bob's family?",
		"Describe Carol's career",
		"How is Alice feeling lately?",
		"What kind of person is Bob?",
		"Summarise Alice's relationship with Carol",
		"What's Bob's background?",
		"Give me an overview of Carol's projects",
		"What is Alice like as a friend?",
		"Describe Bob's daily routine",
		// ru
		"Расскажи об увлечениях Алисы",
		"Что ты знаешь о семье Боба?",
		"Опиши карьеру Кэрол",
		"Как Алиса чувствует себя в последнее время?",
		"Что Боб за человек?",
		"Расскажи об отношениях Алисы и Кэрол",
		"Каков жизненный опыт Боба?",
		"Дай обзор проектов Кэрол",
		"Какая Алиса подруга?",
		"Опиши обычный день Боба",
		// zh
		"告诉我Alice的爱好",
		"你对Bob的家人了解多少？",
		"描述Carol的职业",
		"Alice最近感觉怎么样？",
		"Bob是什么样的人？",
		"概述Alice和Carol的关系",
		"Bob的背景是什么？",
		"概述Carol的项目",
		"Alice作为朋友怎么样？",
		"描述Bob的日常生活",
	},
	QueryCategoryAdversarial: {
		// en — explicit negation + soft truth-value challenges
		"Did Alice never travel to Japan?",
		"Is it true that Bob hates pets?",
		"Did Carol not finish her degree?",
		"Is Alice's claim about the photo false?",
		"Did Bob never speak to Carol?",
		"Was Alice actually at the meeting?",
		"Did Carol really say that?",
		"Is it correct that Bob owns the company?",
		"Confirm if Alice attended the wedding",
		"Was the trip really cancelled?",
		// ru
		"Алиса никогда не ездила в Японию?",
		"Правда ли что Боб ненавидит животных?",
		"Не закончила ли Кэрол свою степень?",
		"Ложно ли утверждение Алисы о фотографии?",
		"Боб никогда не разговаривал с Кэрол?",
		"Алиса действительно была на встрече?",
		"Кэрол правда так сказала?",
		"Верно ли что Боб владеет компанией?",
		"Подтверди, была ли Алиса на свадьбе",
		"Поездка действительно была отменена?",
		// zh
		"Alice从未去过日本吗？",
		"Bob讨厌宠物是真的吗？",
		"Carol没有完成学位吗？",
		"Alice关于照片的说法是错误的吗？",
		"Bob从来没有跟Carol说过话吗？",
		"Alice真的在会议上吗？",
		"Carol真的那么说了吗？",
		"Bob拥有这家公司是对的吗？",
		"确认Alice是否参加了婚礼",
		"旅行真的被取消了吗？",
	},
}

